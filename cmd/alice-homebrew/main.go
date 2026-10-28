package main

import (
	"alicehomebrew/internal/homebrew"
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"
)

func main() {
	if e := run(); e != nil {
		log.Fatal(e)
	}
}
func run() error {
	if len(os.Args) < 2 {
		return fmt.Errorf("usage: alice-homebrew station|store|pack|check|export")
	}
	role := os.Args[1]
	if role == "version" {
		fmt.Println(homebrew.Version)
		return nil
	}
	if role == "update-helper" || role == "recover-update" {
		if len(os.Args) != 3 {
			return fmt.Errorf("%s PLAN.json", role)
		}
		if role == "update-helper" {
			return homebrew.RunUpdateHelper(os.Args[2])
		}
		return homebrew.RecoverManagerUpdate(os.Args[2])
	}
	if role == "pack" {
		if len(os.Args) != 4 {
			return fmt.Errorf("pack DIRECTORY OUTPUT.aliceplugin")
		}
		b, e := homebrew.Pack(os.Args[2])
		if e != nil {
			return e
		}
		if e = writeNew(os.Args[3], b); e != nil {
			return e
		}
		fmt.Println(homebrew.Digest(b))
		return nil
	}
	if role == "check" {
		if len(os.Args) != 3 {
			return fmt.Errorf("check PACKAGE.aliceplugin")
		}
		raw, e := os.ReadFile(os.Args[2])
		if e != nil {
			return e
		}
		p, e := homebrew.Parse(raw)
		if e != nil {
			return e
		}
		if e = homebrew.ValidateModule(p); e != nil {
			return e
		}
		fmt.Printf("%s %s %s\n", p.Manifest.ID, p.Manifest.Version, p.SHA256)
		return nil
	}
	if role == "export" {
		if len(os.Args) < 4 || len(os.Args) > 5 {
			return fmt.Errorf("export MODERATOR_DATA OUTPUT.zip [https://github.com/OWNER/REPO]")
		}
		s, e := homebrew.NewStore(os.Args[2], "")
		if e != nil {
			return e
		}
		f, e := os.OpenFile(os.Args[3], os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		repo := ""
		if len(os.Args) == 5 {
			repo = os.Args[4]
		}
		e = s.Export(f, repo)
		f.Close()
		if e != nil {
			os.Remove(os.Args[3])
		}
		return e
	}
	if role != "station" && role != "store" {
		return fmt.Errorf("unknown command")
	}
	flags := flag.NewFlagSet(role, flag.ContinueOnError)
	defaultListen := ":8088"
	if role == "store" {
		defaultListen = "127.0.0.1:8089"
	}
	listen := flags.String("listen", defaultListen, "listen address")
	data := flags.String("data", "./alice-homebrew-data", "private data directory")
	hardware := flags.Bool("hardware-led", false, "enable the Station Mini 1 I2C LED backend when running on that model")
	platform := flags.String("platform", "", "station profile instead of auto-detection: "+strings.Join(homebrew.PlatformIDs(), ", "))
	yamRoot := flags.String("yam-root", "/data/yam", "yam v2 root on the station")
	local := flags.Bool("allow-local-store", false, "allow HTTP catalogs for local testing")
	scanner := flags.String("scanner", "", "path to clamscan (moderator only)")
	managed := flags.Bool("managed", false, "managed Linux service: automatic manager updates and restart")
	if e := flags.Parse(os.Args[2:]); e != nil {
		return e
	}
	if e := os.MkdirAll(*data, 0700); e != nil {
		return e
	}
	token, tokenPath := "", ""
	var e error
	if role == "store" {
		tokenPath = filepath.Join(*data, "admin-token.txt")
		token, e = homebrew.AccessToken(tokenPath)
		if e != nil {
			return e
		}
	}
	debug.SetMemoryLimit(64 << 20)
	var manager *homebrew.Manager
	var store *homebrew.Store
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	managerDone := make(chan struct{})
	if role == "station" {
		device, e := homebrew.DetectDevice(*platform)
		if e != nil {
			return e
		}
		// The I2C driver is Mini 1 hardware; other rings are found in sysfs.
		homebrew.AllowI2CRing = device.ID == "yandexmini"
		manager, e = homebrew.NewManager(*data, *hardware, *local)
		if e != nil {
			return e
		}
		if *hardware {
			// On the station the probe result is the truth.
			device.LEDRing = manager.Ring.Hardware
			if !device.LEDRing {
				fmt.Printf("no supported LED ring on %s; plugins run without LED output\n", device.ID)
			}
		} else {
			manager.Ring.SetPreviewLayout(homebrew.PreviewRingLayout(device.ID))
		}
		manager.Device = device
		manager.Yam = homebrew.NewYam(device)
		manager.Yam.Root = *yamRoot
		manager.Updater, e = homebrew.NewUpdater(*data, *listen, *managed)
		if e != nil {
			return e
		}
		go func() { defer close(managerDone); manager.Run(ctx) }()
		go manager.Updater.Run(ctx)
	} else {
		store, e = homebrew.NewStore(*data, *scanner)
		if e != nil {
			return e
		}
	}
	server := &http.Server{Addr: *listen, Handler: homebrew.NewServer(manager, store, token), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 120 * time.Second, WriteTimeout: 90 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() {
		<-ctx.Done()
		c, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		server.Shutdown(c)
	}()
	fmt.Printf("Alice Homebrew %s: http://%s\n", role, *listen)
	if tokenPath != "" {
		fmt.Printf("Moderator access code is in %s\n", tokenPath)
	}
	e = server.ListenAndServe()
	if e == http.ErrServerClosed {
		if role == "station" {
			<-managerDone
		}
		return nil
	}
	return e
}
func writeNew(path string, b []byte) error {
	if e := os.MkdirAll(filepath.Dir(path), 0755); e != nil {
		return e
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if e != nil {
		return e
	}
	_, e = f.Write(b)
	f.Close()
	return e
}
