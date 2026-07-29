# Supported File Types

`quant` uses an extractor router that dispatches to the appropriate content handler based on file extension or basename. Files that do not match any extractor are silently skipped.

## Plain text and source code

The text extractor handles all common source code, markup, configuration, and data files. It indexes at most the first 8 MiB of each file; larger files are truncated with a warning. Files that appear to be binary are skipped (detected from null bytes or excessive control characters in the first 8 KiB).

### Recognized extensions

| Category | Extensions |
|----------|-----------|
| Plain text / markup | `.txt` `.md` `.mdx` `.rst` `.adoc` `.tex` `.latex` `.org` `.wiki` |
| Go | `.go` `.mod` `.sum` `.tmpl` |
| Python | `.py` `.pyi` `.pyx` `.pxd` |
| JavaScript / TypeScript | `.js` `.ts` `.jsx` `.tsx` `.mjs` `.cjs` `.mts` `.cts` |
| Web | `.html` `.htm` `.css` `.scss` `.sass` `.less` `.vue` `.svelte` `.astro` |
| JVM | `.java` `.kt` `.kts` `.scala` `.groovy` `.gradle` `.clj` `.cljs` |
| .NET | `.cs` `.fs` `.fsx` `.vb` `.csx` |
| C / C++ | `.c` `.h` `.cpp` `.cxx` `.cc` `.hpp` `.hxx` `.hh` `.inl` |
| Systems | `.rs` `.zig` `.nim` `.v` `.odin` |
| Apple | `.swift` `.m` `.mm` |
| Scripting | `.rb` `.php` `.pl` `.pm` `.lua` `.r` `.R` `.jl` `.tcl` `.awk` `.sed` `.perl` |
| Documentation | `.pod` `.man` `.roff` |
| Shell | `.sh` `.bash` `.zsh` `.fish` `.ps1` `.psm1` `.bat` `.cmd` |
| Functional | `.hs` `.ml` `.mli` `.ex` `.exs` `.erl` `.hrl` `.elm` `.purs` `.rkt` `.scm` `.lisp` `.cl` |
| Mobile | `.dart` |
| Config / Data | `.yaml` `.yml` `.json` `.jsonc` `.json5` `.toml` `.xml` `.xsl` `.xslt` `.ini` `.cfg` `.conf` `.env` `.properties` |
| API / Schema | `.graphql` `.gql` `.proto` `.thrift` `.avsc` `.avdl` |
| Database / Query | `.sql` `.prisma` `.hql` `.cql` |
| Infrastructure | `.tf` `.hcl` `.nix` `.dhall` `.dockerfile` `.containerfile` `.makefile` `.cmake` `.just` `.vagrantfile` |
| Data files | `.csv` `.tsv` `.log` |
| Editor / IDE | `.vim` `.el` `.sublime-syntax` |
| Misc | `.diff` `.patch` `.gitignore` `.gitattributes` `.editorconfig` `.babelrc` `.eslintrc` `.prettierrc` `.stylelintrc` |

### Recognized basenames (case-insensitive)

`Makefile` `Dockerfile` `Containerfile` `Rakefile` `Gemfile` `Justfile` `Vagrantfile` `Procfile` `Brewfile` `CMakeLists.txt` `build.gradle` `build.sbt` `package.json` `tsconfig.json` `Cargo.toml` `go.mod` `go.sum` `requirements.txt` `Pipfile` `setup.py` `setup.cfg` `pyproject.toml` `tox.ini` `.gitignore` `.gitattributes` `.editorconfig` `.dockerignore` `.npmignore` `.eslintignore` `LICENSE` `LICENCE` `COPYING` `AUTHORS` `CHANGELOG` `CHANGES` `HISTORY` `NEWS` `README` `CONTRIBUTING` `TODO`

## Document formats

PDF, notebook, HTML, Office/Open XML, OpenDocument, and RTF extractors reject files over 100 MiB. ZIP-based document formats also reject individual expanded entries over 100 MiB.

### Jupyter notebooks (`.ipynb`)

Extracts code cells and markdown cells. Code outputs (text only) are captured below their source cell. Each cell is marked with a type-specific header: `[Markdown Cell N]`, `[Code Cell N]`, or `[Raw Cell N]`.

### PDF (`.pdf`)

Extracts embedded text with page markers (`[Page N]`). If a PDF contains no extractable text (scanned document), `quant` attempts OCR via `ocrmypdf` as a fallback, controlled by:

- `--pdf-ocr-lang` (default: `eng`) - Tesseract language codes
- `--pdf-ocr-timeout` (default: `2m`) - OCR timeout per file

OCR requires `ocrmypdf` to be installed and on `PATH`.

### Office/Open XML

| Format | Extensions |
|--------|-----------|
| Word | `.docx` `.docm` `.dotx` `.dotm` |
| PowerPoint | `.pptx` `.pptm` `.ppsx` `.ppsm` `.potx` `.potm` |
| Excel | `.xlsx` `.xlsm` `.xltx` `.xltm` `.xlam` |

Word headers and footers are included with section markers. Slides are marked with `[Slide N]` and include speaker notes under `[Notes]` when present. Spreadsheet sections are marked with `[Sheet <name>]`; cell references, values, and formulas are preserved where available.

### OpenDocument

| Format | Extensions |
|--------|-----------|
| Text | `.odt` |
| Spreadsheet | `.ods` |
| Presentation | `.odp` |

### Rich Text Format (`.rtf`)

Plain text is extracted from RTF files. Formatting is discarded.

## Filtering

You can restrict which files are indexed using include/exclude patterns in the YAML config or via nested `.gitignore` files, which are automatically respected. Scans do not follow symlinks and skip every hidden directory, although supported hidden files such as `.env` can be indexed. Include patterns cannot override `.gitignore` or hidden-directory exclusions.

See [docs/configuration.md](configuration.md#includeexclude-patterns) for pattern syntax.
