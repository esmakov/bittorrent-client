# Another BitTorrent Client (ABC)

The [mktorrent](github.com/pobrn/mktorrent/) package is required to build test code.

## Installation
```console
go install .
```

## Usage
```console
bittorrent-client <run>|<start>|<stop>|<remove>|<repair> [<add>|<info>|<parse> <path/to/file.torrent>]
```

- The following commands are supported:
  - run: Run client in the foreground
  - start: Run in the background 
  - stop: Stop working in the background
  - remove: Remove a torrent from the client
  - repair: Remove corrupted pieces of data
  - add: Add a new torrent and select the files you want
  - info: Display information about the torrent (total size, # of files, etc.)
  - parse: Display the .torrent file parse tree

## Features
- Parses Bencoded .torrent files and tracker responses
- Supports Bencoding structured data for serialization
- Parses and implements the [peer message protocol](https://wiki.theory.org/BitTorrentSpecification#Messages)
- Downloads single or multi-file torrents
- Checks existing data on disk and picks up from where you left off
- Web UI displays logs and progress

### Supported BEPs (BitTorrent Enhancement Proposals)
- 3: Basic BitTorrent protocol (in progress)
- 23: Tracker Returns Compact Peer Lists

## TODOs
- Test more than one torrent at a time
- Send keepalives
- Remove multiple torrents at once
- Blacklist misbehaving peers
- Stream bitfield changes to client
- Persist number of bytes uploaded
- Colored output for parse subcommand
- Add and remove torrents without restarting
- User-specified logging level, paths for downloads, path for config file
- Use already-written bencode package to generate .torrent files (instead of pobrn/mktorrent)

### Optimizations
- Better piece download strategies (rarest first)
- Better peer selection strategies (optimistic unchoking)

### Protocol
- Support the dictionary model for tracker responses listing available peers
#### Extensions
- DHT
- Peer Exchange
