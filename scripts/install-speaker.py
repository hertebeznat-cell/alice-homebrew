"""Install the built service on a rooted Yandex station over an isolated ADB server.

No partition flashing. Default mode checks requirements only. --install writes
the service under /data; --autostart additionally installs a known init hook.
"""
from pathlib import Path
import argparse
import hashlib
import json
import os
import re
import shutil
import subprocess
import time
import urllib.request

# Same order and patterns as internal/homebrew/device.go and yam install.sh.
PLATFORMS=[('yandexmini_2',('yandex,station-mini2','station-mini2')),('cucumber',('yandex,cucumber-dvt2','cucumber')),
    ('yandexmidi',('yandex,station-midi',)),('orion',('yandex,orion-pvt','orion')),('pickle',('yandex,pickle-evt','pickle')),
    ('bergamot',('yandex,station-mini3','bergamot')),('saturn',('yandex,saturn','saturn')),('yandexstation',('tulip-d1','tulip')),
    ('yandexstation_2',('yandexstation_2','station-max')),('jbl_link_music',('mediatek,mt8167','jbl')),('mango',('mango',)),
    ('plum',('plum',)),('yandexmicro',('yandex,station-micro','station-micro','yandexmicro')),('yandexmini',('amlogic,axg','yndx-0004'))]
BINARIES={'aarch64':'alice-homebrew-linux-arm64','armv7l':'alice-homebrew-linux-arm','armv8l':'alice-homebrew-linux-arm'}

def detect_platform(identity):
    identity=identity.lower().replace(' ','')  # Mini 1 reports "amlogic, axg"
    for platform,patterns in PLATFORMS:
        if any(p in identity for p in patterns): return platform
    return ''

def main(argv=None,log=print):
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--adb',default=shutil.which('adb') or shutil.which('adb.exe'))
    parser.add_argument('--key',type=Path)
    parser.add_argument('--serial')
    parser.add_argument('--adb-port',type=int,default=5037)
    project=Path(__file__).resolve().parents[1]
    parser.add_argument('--binary',type=Path,help='service binary; chosen by the station architecture when omitted')
    parser.add_argument('--platform',help='station profile when auto-detection fails, e.g. yandexmini_2')
    parser.add_argument('--install',action='store_true')
    parser.add_argument('--autostart',action='store_true')
    args=parser.parse_args(argv)
    if args.autostart and not args.install: parser.error('--autostart requires --install')
    try:
        settings=json.loads((Path.home()/'.config/StationMiniRoot/settings.json').read_text(encoding='utf-8'))
    except (OSError,ValueError): settings={}
    adb=args.adb or settings.get('adb')
    if not adb or not Path(adb).is_file(): raise RuntimeError('Specify --adb from Android Platform-Tools')
    key=args.key or Path(settings.get('work',str(Path.home()/'StationMiniRoot')))/'keys/adbkey'
    env=os.environ.copy()
    if key.is_file(): env['ADB_VENDOR_KEYS']=str(key)
    env.pop('ADB_SERVER_SOCKET',None)
    prefix=[str(adb),'-P',str(args.adb_port)]
    def run(command,timeout=30):
        try:
            r=subprocess.run(prefix+command,env=env,capture_output=True,timeout=timeout,
                             creationflags=getattr(subprocess,'CREATE_NO_WINDOW',0))
        except subprocess.TimeoutExpired as exc:
            raise RuntimeError(f'ADB timed out after {timeout}s: {command[0]}. Check the USB connection and retry.') from exc
        if r.returncode: raise RuntimeError(r.stderr.decode('utf-8',errors='replace'))
        return r.stdout.decode('utf-8',errors='replace').replace('\r','').strip()
    try:
        devices=[line.split()[0] for line in run(['devices']).splitlines()[1:] if len(line.split())>1 and line.split()[1]=='device']
        serial=args.serial or (devices[0] if len(devices)==1 else '')
        if not serial or serial not in devices: raise RuntimeError('Connect one authorized speaker or specify --serial')
        target=['-s',serial]
        def shell(script):
            output=run(target+['shell',script+'\n__alice_status=$?\nprintf "\\n__ALICE_EXIT__:%s\\n" "$__alice_status"'])
            lines=output.splitlines()
            if not lines or lines[-1]!='__ALICE_EXIT__:0':
                raise RuntimeError('Speaker command failed: '+output)
            return '\n'.join(lines[:-1]).strip()
        if 'uid=0' not in shell('id'): raise RuntimeError('ADB root is required')
        if shell('if [ -e /system/framework/framework.jar ] || [ -e /system/bin/app_process ]; then echo android; fi')=='android':
            raise RuntimeError('This station runs Android. Alice Homebrew supports only Linux-based Yandex stations; nothing was changed.')
        machine=shell('uname -m')
        if machine not in BINARIES: raise RuntimeError(f'Unsupported station CPU: {machine}')
        identity=shell(r'cat /proc/device-tree/compatible 2>/dev/null | tr "\000" "\n"; getprop ro.product.device 2>/dev/null; getprop ro.product.model 2>/dev/null; true')
        platform=args.platform or detect_platform(identity)
        if not platform: raise RuntimeError('Station model not recognized; pass --platform (see README). Identity: '+' '.join(identity.split())[:200])
        if args.platform and args.platform not in dict(PLATFORMS): raise RuntimeError('Unknown --platform '+args.platform)
        log(f'Station: {platform} ({machine})')
        if not args.binary:
            name=BINARIES[machine]
            args.binary=next((p for p in (project/'dist'/name,project/'bin'/name) if p.is_file()),project/'bin'/name)
        if not args.binary.is_file(): raise RuntimeError(f'Build {args.binary.name} first (scripts/build-distribution.py)')
        rows=shell('df -k /data').splitlines()
        values=rows[-1].split()
        if len(values)<6 or not values[-1].startswith('/'): raise RuntimeError('Cannot read /data free space')
        free=int(values[-3])*1024
        required=args.binary.stat().st_size+(20<<20)
        log(f'Available /data storage: {free//1048576} MiB; required: {(required+1048575)//1048576} MiB')
        if free<required: raise RuntimeError('Not enough storage. No files were changed. Free space manually and retry.')
        if not args.install:
            log('Requirements checked. Use --install to install; add --autostart for boot integration.')
            return
        root='/data/alice-homebrew'
        if args.autostart:
            previous=shell('if test -e /etc/init.d/S98alice-homebrew; then cat /etc/init.d/S98alice-homebrew; fi')
            if previous and '# Alice Homebrew managed startup hook v1' not in previous:
                raise RuntimeError('An unrelated S98alice-homebrew already exists; refusing to replace it')
        shell(f'mkdir -p {root} && chmod 700 {root}')
        script=Path(__file__).resolve().parent/'start-speaker.sh'
        run(target+['push',str(args.binary),root+'/alice-homebrew.new'],timeout=120)
        run(target+['push',str(script),root+'/start.sh.new'])
        expected=hashlib.sha256(args.binary.read_bytes()).hexdigest()
        if shell('sha256sum '+root+'/alice-homebrew.new').split()[0]!=expected:
            raise RuntimeError('Transferred binary checksum mismatch; the existing service was not changed')
        shell(f'if test -x {root}/start.sh; then {root}/start.sh stop; fi')
        shell(f'chmod 700 {root}/alice-homebrew.new {root}/start.sh.new && mv {root}/alice-homebrew.new {root}/alice-homebrew && mv {root}/start.sh.new {root}/start.sh')
        # A manual profile must survive restarts; auto-detection needs no file.
        shell(f'echo {platform} > {root}/platform' if args.platform else f'rm -f {root}/platform')
        if args.autostart:
            hook=Path(__file__).resolve().parent/'S98alice-homebrew'
            run(target+['push',str(hook),root+'/S98alice-homebrew'])
            shell('mount -o remount,rw /')
            try:
                shell(f'cp {root}/S98alice-homebrew /etc/init.d/S98alice-homebrew && chmod 755 /etc/init.d/S98alice-homebrew && sync')
            finally:
                shell('mount -o remount,ro /')
        shell(root+'/start.sh start')
        port=run(target+['forward','tcp:0','tcp:8088'])
        if not port.isdigit(): raise RuntimeError('Could not create a temporary health-check tunnel')
        for _ in range(20):
            try:
                with urllib.request.urlopen('http://127.0.0.1:'+port+'/api/about',timeout=2) as response:
                    health=json.load(response)
                if health.get('mode')=='station' and health.get('access_code_required') is False: break
            except (OSError,ValueError): pass
            time.sleep(.5)
        else: raise RuntimeError('Service did not become ready; read /data/alice-homebrew/service.log')
        network=shell('ip -4 addr show wlan0')
        addresses=re.findall(r'inet (\d+\.\d+\.\d+\.\d+)/',network)
        address=addresses[0] if addresses else 'SPEAKER_IP'
        url=f'http://{address}:8088'
        log(f'Open {url} in your browser. No access code is required.')
        return url
    finally:
        # Cleanup must not hide a USB error or timeout from the actual operation.
        if args.adb_port != 5037:
            try:
                subprocess.run(prefix+['kill-server'],env=env,capture_output=True,timeout=5,
                               creationflags=getattr(subprocess,'CREATE_NO_WINDOW',0))
            except (OSError,subprocess.TimeoutExpired):
                pass

if __name__=='__main__':
    try: main()
    except Exception as exc: raise SystemExit(str(exc))
