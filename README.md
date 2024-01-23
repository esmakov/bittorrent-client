A minimal bittorrent client with no dependencies outside the Go standard library.
Made purely as a learning project, use at your own risk.

# Usage
`go build`

`./bittorrent-client [command] [path to .torrent file]`

## Commands
- add: Create files needed for torrent and start downloading
- tree: Pretty-print the parse tree of the .torrent file

## Features
- Parses bencoding used in .torrent files and tracker responses
- Supports bencoding structured data for serialization
- Parses [peer message format](https://wiki.theory.org/BitTorrentSpecification#Messages)
- Downloads single or multi-file torrents
- Checks existing data on disk and picks up from where you left off

### Supported BEPs
- 3: Basic BitTorrent protocol (in progress)
- 23: Tracker Returns Compact Peer Lists

## TODOs
### Critical
- Respond to piece requests
- Send "completed" event to tracker

### Optimizations
- Better piece download strategies (rarest first)
- Better peer selection strategies (optimistic unchoking)

### Completeness
- More subcommands: remove, pause/resume
- Support the (so-far-unseen) dictionary model for tracker responses listing available peers
