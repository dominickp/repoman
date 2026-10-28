# repoman

A fast terminal UI for managing locally cloned git repositories: find them,
see what needs attention, and fetch or safely update them in bulk.

## Install

Requires Go 1.26+ and `git` on `PATH`.

```sh
go install github.com/dom/repoman/cmd/repoman@latest
```

Or from a checkout:

```sh
make build      # ./repoman
make install    # installs to ~/.local/bin/repoman
```

`make install` targets `~/.local/bin` so the binary lands somewhere usually
already on `PATH`; override the location with
`make install INSTALL_DIR=/usr/local/bin`. `go install` on its own writes to
`$(go env GOPATH)/bin` (often `~/go/bin`), which may need adding to `PATH`:

```sh
export PATH="$HOME/go/bin:$PATH"
```

## Usage

```sh
repoman                 # start the TUI
repoman -list           # plain-text overview, no TUI
repoman -add-dir ~/src # add a scan directory
repoman -config path    # use an alternative config file
repoman -version
```

On first run repoman seeds its scan directories with conventional clone
locations that exist (`~/Documents/GitHub`, `~/GitHub`, `~/git`, `~/src`,
`~/dev`, `~/code`).

## Development

```sh
make test    # unit + integration tests (they build throwaway git repos)
make vet
make build
```
