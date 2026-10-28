package homebrew

// Version is also used by the release packager. A test build can override it
// with -X; production builds always use the version committed here.
var Version = "6.0.0"

// Codename names the manager generation shown next to the version.
const Codename = "newlife"

// Commit is the short source commit, set by scripts/build-distribution.py
// with -X. Local builds leave it empty.
var Commit = ""
