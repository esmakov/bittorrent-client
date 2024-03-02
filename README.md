A minimal bittorrent client with no dependencies outside the Go standard library.
Made purely as a learning project, USE AT YOUR OWN RISK.

The [mktorrent](github.com/pobrn/mktorrent/) package is required to build test code.

# Usage
```go
go run main <command> <path/to/file.torrent>
```

Where:
- <command> is required and can be one of the following:
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
### Completeness
- More subcommands: remove, pause/resume
- Support the dictionary model for tracker responses listing available peers

### Optimizations
- Better piece download strategies (rarest first)
- Better peer selection strategies (optimistic unchoking)

### Not currently supported
- DHT
- Peer Exchange
