# microsoft/FHIR-Converter sample HL7v2 messages

- Source: https://github.com/microsoft/FHIR-Converter
- Commit: 70fd328e05019142f616a660cf65c6034baaa3c9
- Original path: `data/SampleData/Hl7v2/`
- License: MIT (see `LICENSE` in this directory)

Every file under `messages/` is a byte-for-byte copy of the upstream file
with the same name. Nothing was excluded or renamed. Line endings (LF) and
the UTF-8 byte-order marks present in some files are kept as upstream has
them; the corpus test normalizes them when it frames each message for MLLP,
as any sender would.
