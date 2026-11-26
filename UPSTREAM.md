# Upstream

Fork of <https://github.com/ulikunitz/xz> (`master`).

- Forked from: [`b371040`](https://github.com/ulikunitz/xz/commit/b3710402543262b807b5bb3370b310aae8efa2c0), committed 2025-08-31
- Reviewed through: [`6ead826`](https://github.com/ulikunitz/xz/commit/6ead826b4d3c7c9856f2daa905cf06403b9daddc), reviewed 2026-10-05

Every upstream commit after the fork point, through the reviewed one, is in
exactly one of the tables below. A port is a commit of this repository,
adapted to the fork; nothing is merged from upstream.

## Incorporated

| Upstream commit | Here | What |
| --- | --- | --- |
| [`82b346c`](https://github.com/ulikunitz/xz/commit/82b346cd8be59c38d9c898f43eb1cf9021e1a39f) | [`2514bf4`](https://github.com/forkcloser/go-xz/commit/2514bf4) | `internal/term` falls back to "not a terminal" on unsupported systems |
| [`591e09e`](https://github.com/ulikunitz/xz/commit/591e09e51d9cd7707ca2d3cd126ae7e6b68e5b99) | [`4c88e4f`](https://github.com/forkcloser/go-xz/commit/4c88e4f) | `lzma.ByteReader` retries `(0, nil)` reads |

## Not incorporated

| Upstream commit | Why |
| --- | --- |
| [`ba40d80`](https://github.com/ulikunitz/xz/commit/ba40d80159752f0a291aca9e79cea490daf24b5e) | upstream's v0.5.16 release bookkeeping (version strings, relnotes, TODO.md) |
| [`024f909`](https://github.com/ulikunitz/xz/commit/024f9092972afea64fa5c25157566881bac36938) | `+build` lines for the fallback; this fork uses `go:build` only, on its own narrower platform set |
| [`f5969eb`](https://github.com/ulikunitz/xz/commit/f5969eb5ae921ec9343a77d1ea9cc9812302b0af) | merge commit (of `591e09e`) |
| [`6ead826`](https://github.com/ulikunitz/xz/commit/6ead826b4d3c7c9856f2daa905cf06403b9daddc) | upstream's v0.5.17 release bookkeeping |

## Changes upstream does not have

| Change | Here |
| --- | --- |
| `ParallelReader`: block-concurrent decoding over an `io.ReaderAt`, with `Size`, `io.WriterTo`, cancellation through `Close` and goroutine cleanup when abandoned | [`2a68166`](https://github.com/forkcloser/go-xz/commit/2a68166), [`e8ca46a`](https://github.com/forkcloser/go-xz/commit/e8ca46a), [`e1d38d2`](https://github.com/forkcloser/go-xz/commit/e1d38d2), https://github.com/forkcloser/go-xz/pull/127 |
| Decoding about twice as fast as upstream, allocating per stream rather than per operation (`PERF.md`) | [`e3a30e8`](https://github.com/forkcloser/go-xz/commit/e3a30e8), [`9940ee5`](https://github.com/forkcloser/go-xz/commit/9940ee5), https://github.com/forkcloser/go-xz/pull/125 |
| Error classification: `xz.ErrCorrupt`, `ErrUnsupported`, `ErrClosed` and `lzma.ErrCorrupt`, `ErrUnsupported`; every invalid input matches the corrupt sentinel, I/O errors pass through, a stream cut short is `io.ErrUnexpectedEOF` where upstream decoded a file cut after its last block as complete | https://github.com/forkcloser/go-xz/pull/8 |
| Hostile index contents are bounded before any allocation or loop bound; decode buffers grow with the output produced; the decoder dictionary grows on demand | [`e999b1a`](https://github.com/forkcloser/go-xz/commit/e999b1a), [`b31d853`](https://github.com/forkcloser/go-xz/commit/b31d853), [`ae1481b`](https://github.com/forkcloser/go-xz/commit/ae1481b), [`50b4f38`](https://github.com/forkcloser/go-xz/commit/50b4f38), [`9fbd72d`](https://github.com/forkcloser/go-xz/commit/9fbd72d) |
| `Writer`, `lzma.Writer` and `lzma.Writer2` keep their first error and return it from every later call (upstream's `Writer2` panicked on the `Write` after a failed flush); `Writer2.Close` reports a failed last flush; LZMA2 with a dictionary smaller than a chunk falls back to a compressed chunk | https://github.com/forkcloser/go-xz/pull/8, [`2a92eca`](https://github.com/forkcloser/go-xz/commit/2a92eca) |
| `WriterConfig.NoCheckSum`; `lzma.Reader2.Reset` and `DictCap`; `lzma.NewReader` buffers a plain `io.Reader` | https://github.com/forkcloser/go-xz/releases/tag/v1.0.0 |
| v2 (`github.com/forkcloser/go-xz/v2`): readers and writers configured by options (`WithWorkers`, `WithMaxDictSize`, `WithSingleStream`; `WithDictSize`, `WithProperties`, `WithBlockSize`, `WithCheck`, `WithFilters`); one `Reader` that decodes blocks concurrently given workers; `Check` and `Filter` types; the writer applies the BCJ and delta filters; `lzma.NewReader2` takes the dictionary size; `lzma.ErrSizeExceeded`; the configuration structs, `Verify`, `ParallelReader`, the wire check bytes, `MatchAlgorithm`, `ByteReader`, `EncodeDictCap`, `DecodeDictCap` and `ErrDictSize` gone | https://github.com/forkcloser/go-xz/pull/188 |
| Removed: the `xio` package, `lzma.BinaryTree` (it emitted match distances the decoder rejects), `cmd/xb`, and later the `gxz` command, which [compress](https://github.com/forkcloser/go-compress)'s `xz` replaces | [`c7fe3db`](https://github.com/forkcloser/go-xz/commit/c7fe3db), https://github.com/forkcloser/go-xz/pull/8, https://github.com/forkcloser/go-xz/pull/119 |
| Terminal detection on linux, darwin, FreeBSD and Windows only | https://github.com/forkcloser/go-xz/pull/23 |
| The LZMA and LZMA2 streams' edges hold the package's contract: seven fixes at chunk ends, cut streams, `Reset`, zero uncompressed size, `Close` twice | https://github.com/forkcloser/go-xz/pull/120 |
| The LZMA2 decoder refuses what liblzma refuses | https://github.com/forkcloser/go-xz/pull/121 |
| A complete file that is not a valid `.xz` file is corrupt, in both readers | https://github.com/forkcloser/go-xz/pull/123 |
| A block header is read into a buffer its reader owns | https://github.com/forkcloser/go-xz/pull/126 |
| The writer encodes blocks concurrently: `WithWorkers` on `NewWriter`, the file the sequential writer writes at that block size | https://github.com/forkcloser/go-xz/pull/199 |
| A `Reader` keeps its first error (upstream's goes on from where the input stands, and after a block header with a wrong CRC returns the data that follows), and `Close` closes it | https://github.com/forkcloser/go-xz/pull/201 |
| `lzma.Reader2.Read` returns what a flushed chunk held (upstream's waits for the next chunk until its buffer is full) | https://github.com/forkcloser/go-xz/pull/203 |
| Stream padding and empty streams cost what they hold to read (upstream reads padding four bytes a `Read`) | https://github.com/forkcloser/go-xz/pull/204 |
| `Writer` and `lzma.Writer2` hold their underlying writer to its count: a short count without an error is `io.ErrShortWrite` (upstream reports the file written) | https://github.com/forkcloser/go-xz/pull/206 |
| A classic `.lzma` header states a dictionary size XZ Utils reads (upstream writes the size as given, and `xz` does not recognise the file) | https://github.com/forkcloser/go-xz/pull/210 |
| A block's encoder is sized by its block: at the default dictionary, a megabyte written in blocks of 4 KiB allocates 38 MB (upstream allocates 12.9 GB) | https://github.com/forkcloser/go-xz/pull/209 |
| An input that answers no byte and no error a hundred times in a row ends the readers with `io.ErrNoProgress` (upstream's `Reader` goes on asking, at 116 of the 252 byte positions of a three-block file) | https://github.com/forkcloser/go-xz/pull/211 |
| A writer given no data writes no block: the 32 bytes `xz` writes for an empty input (upstream writes an empty block: 56 bytes) | https://github.com/forkcloser/go-xz/pull/212 |
| `lzma.Reader.Header` returns the header alone, and a `Properties` value prints as `LC 3 LP 0 PB 2` (upstream's `Header` returns an `ok` with it, and its `Properties` value prints as `{3 0 2}`) | https://github.com/forkcloser/go-xz/pull/214 |
| An end of input that wraps `io.EOF` ends a file as `io.EOF` does (upstream returns the wrapping error, at the end of a whole file too) | https://github.com/forkcloser/go-xz/pull/218 |
| A match that is costly to encode at the end of an LZMA2 chunk is written like any other (upstream's `xz.Writer` and `lzma.Writer2` return nil from `Write` and `Close` and write a file that lacks what followed: on the input of the fix's test, the last 115,704 of 1,318,299 bytes, and a `.xz` file that fails its block check) | https://github.com/forkcloser/go-xz/pull/231 |
