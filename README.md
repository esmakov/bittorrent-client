## Features
- Parses bencoded metainfo (.torrent) files and bencoded tracker responses
- Parses peer message format
- Supports bencoding structured data for serialization

## TODOs
### Critical
- Reassemble fragmented peer messages (likely with gopacket
- Assemble chunks into pieces
- Save pieces to disk
- Multi-file torrent support

### Optimizations
- Better piece download strategies (rarest first)
- Better peer selection strategies (optimistic unchoking)

### Completeness
- Support the dictionary model for tracker responses listing available peers
