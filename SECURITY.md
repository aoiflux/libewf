# Security Policy

## Threat model

libewf parses **untrusted input by design**. A forensic image is evidence
produced by an unknown party on an unknown machine; it may be truncated,
bit-rotted, or deliberately malformed to attack the tool examining it. Every
length, offset and count this library acts on is read from that input.

The guarantees the library aims to provide for any input, valid or not:

- **No panics.** A malformed image produces an error, never a crash. A panic in
  a parser is a denial of service against the examiner, and in a pipeline it can
  destroy an entire analysis run.
- **Bounded allocation.** Sizes read from the image are checked before being
  used to allocate. A corrupt length field cannot drive an out-of-memory abort.
- **No silent misdecoding.** Where the library cannot decode something
  correctly it returns an error rather than plausible-looking bytes. Wrong
  evidence presented as right is the worst outcome this library can produce, and
  it is treated as a more serious defect than a crash.
- **Read-only.** No code path writes to the image or anywhere else. `Create` is
  unimplemented by design.
- **Encrypted images fail closed.** They are rejected rather than decoded, so
  ciphertext is never returned as device content.

What is explicitly **not** in scope: the library does not attempt to detect that
an image has been tampered with. `Verify` recomputes the acquisition digests
stored in the container, but those digests are part of the same file — an
attacker who alters the data can alter them too. Use externally recorded hashes
for chain-of-custody.

## Reporting a vulnerability

Report suspected vulnerabilities privately through GitHub's **Report a
vulnerability** button on the Security tab of
<https://github.com/aoiflux/libewf>, rather than in a public issue.

Please include:

- what you observed (panic, hang, excessive allocation, incorrect decode);
- a minimal input that reproduces it, or a generator for one;
- the version or commit, and your Go version and platform.

A reproducing input is by far the most useful thing you can send. If it contains
real evidence, please reduce it to synthetic data that still triggers the
behaviour — do not send case material.

## What counts as a vulnerability

Treated as security issues:

- a panic, hang, or unbounded allocation reachable from `Open`, `ReadAt`,
  `Metadata` or `Verify` on any input;
- an image that decodes to incorrect bytes without an error;
- reading outside the supplied `io.ReaderAt`, or any write.

Not security issues, though still worth reporting as ordinary bugs:

- an unsupported format variant that is rejected with a clear error;
- an image this library declines to open that another tool accepts;
- the documented gaps in [README.md](README.md#limitations).

## Testing for these properties

The properties above are enforced in CI rather than asserted:

- `FuzzOpenAndRead` fuzzes `Open` and `ReadAt`, checking for panics and for
  short reads that wrongly report success. It runs on every pull request and for
  longer nightly.
- `TestTruncatedAndCorruptInputsDoNotPanic` sweeps truncations and single-byte
  corruptions across a synthetic image.
- `TestCorpus` decodes images from real acquisition tools and compares the
  result byte-for-byte against the raw device that was acquired, so a silent
  misdecode fails the build.
- The full suite runs under `-race` on Linux, Windows and macOS.

If you are adding a parser, add a case to the fuzz seed corpus and a malformed
input test alongside it.
