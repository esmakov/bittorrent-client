A simple CLI bittorrent client

The [mktorrent](github.com/pobrn/mktorrent/) package is required to build test code.

# Usage
```shell
go run main <add>|<info>|<parse> <path/to/file.torrent>
```

- The following commands are supported:
  - add: Add a new torrent and start downloading.
  - info: Display information about the torrent (total size, # of files, etc.)
  - parse: Display the torrent file structure.
- <path/to/file.torrent> is the required path to a .torrent file.

## Features
- Parses Bencoded .torrent files and tracker responses
- Supports Bencoding structured data for serialization
- Parses and implements the [peer message protocol](https://wiki.theory.org/BitTorrentSpecification#Messages)
- Downloads single or multi-file torrents
- Checks existing data on disk and picks up from where you left off

### Supported BEPs (BitTorrent Enhancement Proposals)
- 3: Basic BitTorrent protocol (in progress)
- 23: Tracker Returns Compact Peer Lists

## TODOs
- Spawn child process to run in background (like Caddy)
- Handle more than one torrent at a time
- Send keepalives

- Cleaner UI (maybe take cues from Ezio)
- Blacklist misbehaving peers
- Specify download directory
- More interesting bitmask visualization?
- Use already-written bencode package to generate .torrent files (instead of pobrn/mktorrent)
- Set log level (debug, info, errors)

### Optimizations
- Better piece download strategies (rarest first)
- Better peer selection strategies (optimistic unchoking)

### Protocol
- Support the dictionary model for tracker responses listing available peers
#### Extensions
- DHT
- Peer Exchange
