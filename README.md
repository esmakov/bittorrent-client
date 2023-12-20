## Features
- Parses bencoded metainfo (.torrent) files and bencoded tracker responses
- Parses peer message format
- Supports bencoding structured data for serialization
- Reassembles peer messages from across packet boundaries in a TCP stream (needs testing)

## TODOs
### Critical
- Request piece fragments aka "chunks"
- Save pieces to disk

### Optimizations
- Better piece download strategies (rarest first)
- Better peer selection strategies (optimistic unchoking)

### Completeness
- Support the dictionary model for tracker responses that list available peers
