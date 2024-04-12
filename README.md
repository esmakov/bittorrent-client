A minimal bittorrent client made purely as a learning project, USE AT YOUR OWN RISK.

The [mktorrent](github.com/pobrn/mktorrent/) package is required to build test code.

# Usage
```console
go run main <add>|<tree> <path/to/file.torrent>
```

- Two commands are supported:
  - add: Add a new torrent and start downloading.
  - tree: Display the torrent file structure.
- <path/to/file.torrent> is the required path to a .torrent file.

## Features
- Parses bencoded .torrent files and tracker responses
- Supports bencoding structured data for serialization
- Parses [peer message format](https://wiki.theory.org/BitTorrentSpecification#Messages)
- Downloads single or multi-file torrents
- Checks existing data on disk and picks up from where you left off

### Supported BEPs
- 3: Basic BitTorrent protocol (in progress)
- 23: Tracker Returns Compact Peer Lists

## TODOs
- Run in background
- Specify destination directory
- Specify log level and log destination
- More subcommands: remove, pause/resume

### Optimizations
- Better piece download strategies (rarest first)
- Better peer selection strategies (optimistic unchoking)

### Protocol
- Support the dictionary model for tracker responses listing available peers
#### Extensions
- DHT
- Peer Exchange
