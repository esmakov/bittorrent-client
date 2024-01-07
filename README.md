# Usage
`go build`

`bittorrent-client add [path to .torrent file]`

## Command line options
`-print` to print parse tree of .torrent file

## Features
- Parses bencoded metainfo (.torrent) files and bencoded tracker responses
- Parses [peer message format](https://wiki.theory.org/BitTorrentSpecification#Messages)
- Supports bencoding structured data for serialization
- Sends handshake and (random) piece request messages

## TODOs
### Critical
- Reassemble peer message segments (likely with gopacket/tcpassembly or gopacket/reassembly)
- Assemble chunks into pieces
- Save pieces to disk
- Multi-file torrent support

### Optimizations
- Better piece download strategies (rarest first)
- Better peer selection strategies (optimistic unchoking)

### Completeness
- Support the dictionary model for tracker responses listing available peers
