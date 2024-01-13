A minimalist (read: lacking features) bittorrent client with no dependencies outside the Go standard library.

# Usage
`go build`

`./bittorrent-client add [path to .torrent file]`

## Command line options
`-print` to print parse tree of .torrent file

## Features
- Parses bencoded metainfo (.torrent) files and bencoded tracker responses
- Parses [peer message format](https://wiki.theory.org/BitTorrentSpecification#Messages)
- Supports bencoding structured data for serialization

### Supported BEPs
- 3: Basic BitTorrent protocol (in progress)
- 23: Tracker Returns Compact Peer Lists

## TODOs
### Critical
- Assemble chunks into pieces
- Save pieces to disk
- Multi-file torrent support

### Optimizations
- Better piece download strategies (rarest first)
- Better peer selection strategies (optimistic unchoking)

### Completeness
- More subcommands: remove, pause/resume
- Support the dictionary model for tracker responses listing available peers
