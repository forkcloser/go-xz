# xz

The [`.xz` file format](https://tukaani.org/xz/xz-file-format.txt) for
Go, read and written, with the classic `.lzma` format and raw LZMA2 beside
it: pure Go, with no dependency and no cgo. The reader decodes every filter
the format defines, and both reader and writer work on several blocks of a
file at once.

This module began as a fork of
[ulikunitz/xz](https://github.com/ulikunitz/xz). What was taken from
upstream, what was changed, and where the two have since parted is recorded
in [UPSTREAM.md](./UPSTREAM.md); from v2 the API is this module's own. The
command-line tool built on it is
[forkcloser/go-compress](https://github.com/forkcloser/go-compress)'s `xz`.

## Install

```
go get github.com/forkcloser/go-xz/v2
```

Go 1.26 or later. The module requires nothing but the standard library.

## Packages

| Package | What it is |
| --- | --- |
| `xz` | `.xz` files: one `Reader` and one `Writer`, configured by options. |
| `lzma` | The classic `.lzma` format (`Reader`, `Writer`) and the LZMA2 chunk sequence a `.xz` block carries (`Reader2`, `Writer2`). |

## Reading

```go
f, err := os.Open(name)
if err != nil {
	return err
}
defer f.Close()

r, err := xz.NewReader(f, xz.WithWorkers(0)) // 0: as many blocks at once as GOMAXPROCS
if err != nil {
	return err
}
defer r.Close()

if _, err := io.Copy(dst, r); err != nil {
	return err
}
```

A `Reader` reads a whole file: one stream or several, concatenated with or
without padding between them. `WithSingleStream` makes it read one stream and
report anything after it as corruption. It decodes LZMA2, the eight
branch/call/jump filters (x86, PowerPC, IA-64, ARM, ARM Thumb, SPARC, ARM64,
RISC-V) and delta, in chains of up to four, under the None, CRC32, CRC64 and
SHA-256 checks.

With workers, blocks decode concurrently when the input is an `io.ReaderAt`
that is also an `io.Seeker`, as a file is, and the file has two blocks or
more. Anything else decodes in order on one worker: a stream, a file of one
block, a reader of a single stream. Workers change how fast a file is read
and what it costs in memory, not the data: a reader with workers holds whole
decoded blocks, about twice the workers plus a few of them, and a dictionary
for each worker, and must be closed when abandoned before `io.EOF`. A block
is as large as its file made it, so the reader uses as many of its workers
as keep all of that within a memory limit, and reads in order a file whose
blocks are too large for two. The limit is `WithMemoryLimit`'s, and 1 GiB
without it, on every machine: how much of a machine a reader may take is for
the program to say, and the reader does not look.

The error `Read` returns is classified, to be matched with `errors.Is`:

| Error | What it says |
| --- | --- |
| `io.EOF` | The file has ended, and was whole. |
| `xz.ErrCorrupt` | The input is not a valid `.xz` file: bad magic, a failed check, sizes that disagree, a reserved field that is set. |
| `xz.ErrUnsupported` | The input may be well formed and this package does not decode it: a check or a filter the format reserves, a dictionary above `WithMaxDictSize`. |
| `io.ErrUnexpectedEOF` | The input ends inside a stream. |
| `xz.ErrClosed` | The reader was closed before it had ended or failed. |
| any other | The underlying reader's own error, as it is. |

`WriteTo` returns the same, nil where `Read` returns `io.EOF`, and besides
them the error of the writer it is given, which it holds to its count: a
write that takes less than it was given with no error is `io.ErrShortWrite`.
An option a constructor refuses matches none of the sentinels.

The first error is final: every later `Read` returns it, `io.EOF` included.
`Read` may return data beside an error; beside one that reports corrupt or
cut input, the data is to be discarded, not taken for a correct prefix.

The dictionary is allocated as the data fills it, never at the size a header
declares. `WithMaxDictSize` refuses a block that asks for a larger dictionary
than a reader of untrusted input wants to allow. It does not bound the whole
decoded blocks a reader with workers holds.

## Writing

```go
w, err := xz.NewWriter(dst)
if err != nil {
	return err
}

if _, err := io.Copy(w, src); err != nil {
	return err
}

if err := w.Close(); err != nil { // the last block, the index and the footer
	return err
}
```

| Option | What it sets | Default |
| --- | --- | --- |
| `WithCheck` | The integrity check of every block: `CheckNone`, `CheckCRC32`, `CheckCRC64`, `CheckSHA256`. | CRC64 |
| `WithDictSize` | The dictionary size, `lzma.MinDictSize` to `lzma.MaxDictSize`. | 8 MiB |
| `WithProperties` | The literal context bits, literal position bits and position bits. | 3, 0, 2 |
| `WithBlockSize` | A new block every so many uncompressed bytes. | one block |
| `WithFilters` | Up to three of the BCJ filters and delta, applied before LZMA2. | none |
| `WithWorkers` | How many blocks are encoded at once, 0 for GOMAXPROCS. | 1 |

```go
w, err := xz.NewWriter(dst,
	xz.WithWorkers(0),
	xz.WithCheck(xz.CheckSHA256),
	xz.WithFilters(xz.Filter{ID: xz.ARM64}), // for arm64 machine code
)
```

A writer given workers cuts the data into blocks of `WithBlockSize`, or
without it of three times the dictionary size and at least 1 MiB, as `xz -T`
does, and encodes that many at once. The file is the one a writer without
workers writes at that block size, byte for byte, whatever the number of
workers. Each block being encoded is held in memory with what it compresses
to.

The first error from the underlying writer is final, and the stream cannot
be completed after it. A `Writer` is for one goroutine at a time, with
workers too.

## Classic LZMA and LZMA2

```go
r, err := lzma.NewReader(src)                    // a .lzma file
w, err := lzma.NewWriter(dst, lzma.WithSize(n))  // the header states the size

r2, err := lzma.NewReader2(src, dictSize)        // the LZMA2 chunks of a container
w2, err := lzma.NewWriter2(dst)
```

Without `WithSize` a classic writer leaves the size unknown and ends the
stream with a marker. `Writer2` buffers: `Flush` or `Close` writes what it
holds.

The `.lzma` format has no magic, so `lzma.NewReader` cannot tell a header
from other bytes, and what is not one may declare any dictionary and any
size. The dictionary is allocated as the data fills it; `WithMaxDictSize`
refuses a header that declares more than the caller allows; and
`Reader.Header` returns what a header declared. `lzma.ValidHeader` is a
heuristic for telling the format of a file, not a check of it.

The package's errors are its own: `lzma.ErrCorrupt` and `lzma.ErrUnsupported`
as in `xz`, and `lzma.ErrSizeExceeded` for a write beyond the size a classic
header declared.

## Migrating from v1

v1 is `github.com/forkcloser/xz`, through v1.1.2, under the repository's
former name; so are v2's release candidates through v2.0.0-rc.3, as
`github.com/forkcloser/xz/v2`. The formats are the standard ones in both.
`xz.NewReader(r)`, `xz.NewWriter(w)` and their `lzma`
counterparts compile unchanged but for `lzma.NewReader2`; the configuration
structs and their `Verify` are gone, and a constructor returns the error an
option earns.

| v1 | v2 |
| --- | --- |
| `import "github.com/forkcloser/xz"`, `.../lzma` | `import "github.com/forkcloser/go-xz/v2"`, `.../v2/lzma` |
| `xz.ReaderConfig{SingleStream: true}.NewReader(r)` | `xz.NewReader(r, xz.WithSingleStream())` |
| `xz.ReaderConfig{DictCap: n}`, a floor | nothing: the dictionary is what each block asks for. `xz.WithMaxDictSize(n)` is a limit, which v1 did not have |
| `xz.NewParallelReader(ra, size)` | `xz.NewReader(io.NewSectionReader(ra, 0, size), xz.WithWorkers(0))`; a file is passed as it is |
| `xz.ParallelReaderConfig{Workers: n}.NewParallelReader(ra, size)` | the same with `xz.WithWorkers(n)`; a count below zero is refused |
| `(*xz.ParallelReader).Size()` | no counterpart |
| `xz.WriterConfig{Properties: &lzma.Properties{LC: lc, LP: lp, PB: pb}}` | `xz.WithProperties(lc, lp, pb)` |
| `xz.WriterConfig{DictCap: n}` | `xz.WithDictSize(n)` |
| `xz.WriterConfig{BlockSize: n}` | `xz.WithBlockSize(n)` |
| `xz.WriterConfig{CheckSum: xz.SHA256}`; `xz.None`, `xz.CRC32`, `xz.CRC64`, `xz.SHA256` | `xz.WithCheck(xz.CheckSHA256)`; `xz.CheckNone`, `xz.CheckCRC32`, `xz.CheckCRC64`, `xz.CheckSHA256` |
| `xz.WriterConfig{NoCheckSum: true}` | `xz.WithCheck(xz.CheckNone)` |
| `BufSize`, `Matcher`, `lzma.MatchAlgorithm` | gone: there is one match finder, and its buffer is not a setting |
| `lzma.ReaderConfig{DictCap: n}.NewReader(r)`, refusing with `*lzma.ErrDictSize` | `lzma.NewReader(r, lzma.WithMaxDictSize(n))`, refusing with `lzma.ErrUnsupported` |
| `h, ok := r.Header()` on a `lzma.Reader` | `h := r.Header()` |
| `(*lzma.Reader).EOSMarker()` | no counterpart |
| `lzma.NewReader2(r)`, `lzma.Reader2Config{DictCap: n}.NewReader2(r)` | `lzma.NewReader2(r, n)`: the dictionary size is required |
| `(*lzma.Reader2).DictCap()` | `(*lzma.Reader2).DictSize()` |
| `lzma.WriterConfig{SizeInHeader: true, Size: n}` | `lzma.WithSize(n)` |
| `lzma.WriterConfig{EOSMarker: true}` | `lzma.WithEOSMarker()` |
| `lzma.WriterConfig` and `lzma.Writer2Config`'s `Properties`, `DictCap` | `lzma.WithProperties(lc, lp, pb)`, `lzma.WithDictSize(n)` |
| `lzma.MinDictCap`, `lzma.MaxDictCap` | `lzma.MinDictSize`, `lzma.MaxDictSize` |
| `lzma.ErrNoSpace` | `lzma.ErrSizeExceeded` |
| `lzma.ByteReader`, `lzma.EncodeDictCap`, `lzma.DecodeDictCap` | gone |

What a call that still compiles does differently is in the notes of the
[releases](https://github.com/forkcloser/go-xz/releases), the breaking changes
under their own heading.

## Performance

`just bench` runs the benchmarks; the eight below are medians of six runs,
on 2026-11-15: an Apple M5 Pro (18 cores), macOS, Go 1.27.4. The corpus is `testdata/enwik7`,
10 MB of English Wikipedia text; MB/s is decimal and counts uncompressed
bytes. Half the range over the median is under 3% for the readers and under
8% for the writers.

| Benchmark | What it runs | MB/s |
| --- | --- | ---: |
| `Reader` | one block, read in order | 99 |
| `ParallelReader/workers=1` | ten blocks of 1 MiB, one worker | 96 |
| `ParallelReader/workers=4` | the same, four workers | 305 |
| `ParallelReader/workers=all` | the same, a worker a CPU | 730 |
| `Writer` | one block, the defaults | 12.6 |
| `WriterWorkers/workers=1` | blocks of 1 MiB, one worker | 19.1 |
| `WriterWorkers/workers=4` | the same, four workers | 43.6 |
| `WriterWorkers/workers=all` | the same, a worker a CPU | 86.9 |

One block compresses the corpus to 33.6% of its size, blocks of 1 MiB to
34.5%: each block starts a dictionary of its own. Reading the one-block file
allocates 126 times, 20 MB in all, most of it the dictionary as it grows.

[PERF.md](./PERF.md) records how the decoder got to this speed: what each
change bought, what was tried and did not work, and where the time goes.

## Correctness

The decoders are held to references that share no code with them: the `lzma`
package to a port of the LZMA SDK's reference decoder with liblzma's LZMA2
chunk rules (`internal/lzmaspec`), the container to a reader and writer made
from the file format specification (`reference_test.go`).

The package is also held to XZ Utils itself, a pinned build of 5.8.4 that
`just test` fetches and requires, on each platform's CI leg. What the writers write
must pass `xz -t` and decode under `xz -dc` to the input; what `xz` writes,
with every preset, check, block layout, filter and format, the readers must
decode to the input; and on a stream with one byte flipped, the package
accepts what `xz` accepts and refuses what it refuses. XZ Utils' decoder
conformance files (`testdata/xzutils`) run too: a good file decodes, a bad
one is refused, and one upstream classes as unsupported is never decoded.
A differential test against ulikunitz/xz (`internal/upstreamdiff`, a module
of its own, so the library never depends on upstream) holds that what either
side compresses the other decompresses to the same bytes, and that this
module refuses whatever upstream refuses.

Nine fuzz targets drive the readers, the classic and LZMA2 decoders and the
round trips, on every CI run, and the race detector runs the suite. The
suite runs on Linux (amd64, arm64), Windows (amd64, arm64) and macOS
(arm64).

## Limitations

- The encoder has one match finder and no presets, and does not match XZ
  Utils. On the corpus above, against XZ Utils 5.8.4 on one thread on the
  same machine: this package writes 33.6% of the input in 0.79 s; `xz -1`
  writes 33.4% in 0.32 s, and `xz -6`, its default, 27.2% in 3.8 s. XZ Utils
  decodes the one-block file in 0.08 s, this package in 0.10 s.
- A block decodes on one core, as the format has it: workers need a file of
  several blocks, and an input they can read by offset.
- A reader with workers holds whole decoded blocks in memory, and a writer
  with workers whole blocks with what they compress to. Neither is bounded by
  the dictionary size. A reader keeps what it holds within its memory limit,
  1 GiB unless `WithMemoryLimit` says otherwise: it uses fewer workers for a
  file of large blocks, and reads in
  order, on one core, a file whose blocks are over a twelfth of the limit.
- A `Reader` reads a file from its start. It is not an `io.Seeker`, though a
  file's index would let a block be reached by its offset.
- No 32-bit target is tested.

## Development

Every task runs through `just`: `just lint` and `just test` are the whole
gate. `just test` fetches the pinned XZ Utils binary for the host on its
first run, into `.build`, and runs the differential test against upstream,
`just test-upstreamdiff`, which fetches upstream's module. `just bench` runs
the benchmarks. The tooling is the organization's; see
[CONTRIBUTING](https://github.com/forkcloser/.github/blob/main/.github/CONTRIBUTING.md).

## Security

Report a vulnerability as the organization's
[SECURITY](https://github.com/forkcloser/.github/blob/main/.github/SECURITY.md)
policy says. This module is not XZ Utils and builds none of it, so
[CVE-2024-3094](https://www.cve.org/CVERecord?id=CVE-2024-3094), the backdoor
in the XZ Utils 5.6.0 and 5.6.1 releases, does not concern it; the tests run
an XZ Utils binary as an oracle, and the library never does.

## License

[BSD 3-Clause](./LICENSE).
