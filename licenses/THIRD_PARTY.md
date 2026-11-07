# Third-party source and notices

Project source uses [Apache-2.0](../LICENSE), with project attribution in [NOTICE](../NOTICE). This does not replace any dependency licence or notice.

This source-only reference does not vendor dependencies or distribute compiled binaries. The Go lockfiles select the runtime closure. The observed upstream license texts are retained below. They keep their own terms.

| Module | Version | Observed license | Retained text |
|---|---|---|---|
| cel.dev/cel-go | v0.32.0 | Apache-2.0 | [license](dependencies/cel.dev__cel-go__LICENSE.txt) |
| cel.dev/expr | v0.25.3 | Apache-2.0 | [license](dependencies/cel.dev__expr__LICENSE.txt) |
| github.com/antlr4-go/antlr/v4 | v4.13.1 | BSD-style; retained license digest identifies exact text | [license](dependencies/github.com__antlr4-go__antlr__v4__LICENSE.txt) |
| go.yaml.in/yaml/v3 | v3.0.4 | MIT and Apache-2.0 by file; see upstream LICENSE | [license](dependencies/go.yaml.in__yaml__v3__LICENSE.txt) |
| golang.org/x/exp | v0.0.0-20240823005443-9b4947da3948 | BSD-style; retained license digest identifies exact text | [license](dependencies/golang.org__x__exp__LICENSE.txt) |
| google.golang.org/genproto/googleapis/api | v0.0.0-20240826202546-f6391c0de4c7 | Apache-2.0 | [license](dependencies/google.golang.org__genproto__googleapis__api__LICENSE.txt) |
| google.golang.org/genproto/googleapis/rpc | v0.0.0-20240826202546-f6391c0de4c7 | Apache-2.0 | [license](dependencies/google.golang.org__genproto__googleapis__rpc__LICENSE.txt) |
| google.golang.org/protobuf | v1.36.10 | BSD-style; retained license digest identifies exact text | [license](dependencies/google.golang.org__protobuf__LICENSE.txt) |
| go.yaml.in/yaml/v3 | v3.0.4 | NOTICE attribution | [license](dependencies/go.yaml.in__yaml__v3__NOTICE.txt) |

The author CLI is fetched separately from its pinned public repository, which retains its Apache-2.0 license and dependency notices. Its source and binaries are not in this tree.

## CI tools

The [Claude review](../.github/workflows/claude-review.yml) workflow downloads one tool on the GitHub runner. The tool is not in this tree, and this project does not distribute it.

| Package | Version | Observed license | Use |
|---|---|---|---|
| @anthropic-ai/claude-code-linux-x64 | 2.1.319 | Proprietary. The package `LICENSE.md` says "All rights reserved" and points to the Anthropic legal agreements at https://code.claude.com/docs/en/legal-and-compliance. | The workflow checks the SHA-512 value of the package, then runs the CLI on the runner with the owner's OAuth token. |

The Apache-2.0 licence of this project does not apply to this tool. The owner's account agreement with Anthropic controls its use. If the workflow changes the CLI version, change this row too.

Before distributing any binary, enumerate its actual linked dependencies and retain required license/NOTICE/copyright material. That includes any optional tool binary selected for distribution. A source-only reference is not permission to omit upstream notices from a later binary release.

Python standard-library and system Git are prerequisites, not bundled software. The workflow/schema sources are credited in docs/PROVENANCE.md; provider schemas and native Tekton code are not copied.
