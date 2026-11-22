# Healdan output mode for the agent: design

Date: 2026-11-22. Status: awaiting approval.

## Intent

Each Hercules/mainframe instance runs one virtual1403 agent. The agent files
every print job as a PDF into a folder of a Healdan matter, with filing
details (class, subclass, author, typist and so on) taken from the agent's
config file. Healdan becomes the storage and distribution system and replaces
virtual1403's own webserver for these deployments. A mainframe user who prints
to a given printer looks in that printer's folder in Healdan, and moves, views
or deletes the document themselves.

## Goals

- A third output mode, `healdan`, beside `local` and `online`.
- All Healdan details live in the agent's config, per output, so one agent
  can serve several printers with a folder each.
- No job is lost when Healdan is down or refuses an upload.
- The Healdan account the agent uses has the least access that works.

## Non-goals

- Retention or clean-up of documents in Healdan.
- Changes to Healdan. The design uses its API as it is today.
- Changes to the webserver, or removing it. Upstream still owns it.
- Routing jobs to different folders by job name or class. One output files
  into one folder.

## Anti-goals

- The agent must never need a Healdan administrator account.
- A wrong password must not lock the account by retrying in a loop.

## Approaches

1. **A `healdan` output mode in the agent (chosen).** The agent renders the
   PDF, as `local` mode does, and posts it to Healdan. No webserver, one
   program per mainframe, and the job name is at hand when the document is
   named.
2. **`local` mode plus a separate uploader watching the output directory.**
   Leaves the agent untouched, but is two programs to run and loses the job
   info except through the file name.
3. **Webserver delivers to Healdan instead of email.** Keeps the component
   these deployments are retiring.

The owner picked 1.

## Constraints from Healdan

Read from Healdan's API contract and source on 2026-11-22; not yet run
against a live stack.

- `POST /api/v1/documents` takes multipart `document` (JSON: `folderId`,
  `name`, `properties`) and `file`, with `Authorization: Bearer <token>`.
  `201` returns the document and its doc #.
- A token comes from `POST /api/v1/auth/tokens` with a username and password.
  It lapses after 30 days unused or a year after sign-in, and when the
  password changes. There is no permanent key for this API.
- The folder must be inside a matter and not the matter's top folder.
- Author and Typist are user references by id and default to the caller.
  `GET /api/v1/users?q=` finds a user by username.
- `GET /api/v1/folders/{id}/form` returns the folder's property fields.

## Design

### Config

New `OutputConfig` fields, read only when `mode: healdan`:

```yaml
mode: healdan
profile: default-green
healdan:
  url: https://dms.example.com        # base address; the agent adds /api/v1
  username: print-mvs1
  password: "..."
  folder_id: 1234                     # the folder to file into
  spool_directory: ./healdan-spool    # PDFs waiting to be filed
  properties:                         # sent as Healdan's `properties`, by name
    docclass: Printouts
    docsubclass: MVS1
    author: jsmith                    # a username; looked up at start
    typist: jsmith
```

`properties` is a free map so it follows whatever property names the
folder's form has. Values for user fields (Author, Typist) are usernames;
every other value is sent as written. `font_file` works as in `local` mode.

### Flow

- **Start:** sign in, fetch the folder's form to learn which properties are
  user fields, and look those usernames up. A config that can't work (bad
  password, folder not found or not fileable, unknown property or user) is
  logged as `ERROR` and that input doesn't start, as a bad `local` output
  directory is handled today.
- **Each job:** render the PDF to the spool directory under
  `v1403-<jobinfo>-<timestamp>.pdf`, then upload it under that name. On `201`
  delete the spooled file and log the doc #.
- **Retry:** a file left in the spool is retried on a timer and at start.
  Network errors, `5xx`, `429` and `503` retry with a growing delay. A `401`
  signs in once more and retries. Any other `4xx` (permission taken away,
  validation) is logged as `ERROR` and the file stays in the spool for a
  person to deal with; it is retried at the next start only.
- **Sign-in failures:** `401 sign-in-failed` or `password-expired` stops
  uploads until restart, to stay clear of Healdan's sign-in throttle (5 wrong
  passwords block a username). Jobs keep spooling.

### Code

- `agent/healdan.go`: `healdanOutputHandler` (a `scanner.PrinterHandler`)
  and a small Healdan client (sign in, form, find user, file document).
- `agent/config.go`, `agent/main.go`: the new fields and mode.
- `agent/config.sample.yaml` and the README: the new mode.
- Tests in `agent/` against an `httptest` server that plays Healdan: happy
  path, `401` then re-sign-in, retry after `503`, a `403` left in the spool.
  These are the agent's first tests.
- No new dependencies; `net/http` and `mime/multipart` are enough.

## The Healdan account

The least access that Healdan's checks allow:

- **An ordinary password account**, one per agent. Not read-only (a
  read-only account can't hold Write), not in `admin`, and in no group but
  Everyone, which Healdan puts every ordinary account in.
- **On the target folder: Read and Write, nothing else** (a Custom row with
  those two ticked). Read is needed or the folder answers `404`; Write is
  what filing checks. Not Add, Rename, Move, Delete, Download, Preview,
  Print, Email or Security.
- **On the folders above it:** Healdan lets a folder inside a matter narrow
  its parent's list but never widen it, so the matter's own folder must list
  the account with at least Read and Write too. To keep the account out of
  the matter's other folders, either dedicate the matter to print output, or
  give those folders their own permissions without it.
- **On the Store:** Read, if Healdan's "a matter's readers must be able to
  read its Store" rule counts a person's row. To be confirmed on a live
  stack.

What that access still allows, since Healdan has no narrower permission:
the account can list the folder and see documents' names and properties
there, change their properties, check them out and save new versions. It
can't download, delete, move or rename them. It also has whatever Everyone
is granted elsewhere, so check what Everyone can reach.

Set the account's password not to expire, if the stack expires passwords.

## Open points to confirm on a live Healdan stack

- The Store rule for a person's row, above.
- That `GET /api/v1/users` and the folder form answer for an account with
  only Read and Write.
- The exact property names for class and subclass on the target folder's
  form.

## Left to the builder

Retry timings, the spool's file layout, log wording (within CLAUDE.md's
conventions), and how the client is split inside `agent/healdan.go`.
