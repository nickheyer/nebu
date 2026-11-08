# Settings

Host label and appearance, sources, tool servers, accounts, API tokens.

Route: `/settings/general`, `/settings/sources`, `/settings/tools`, `/settings/accounts`, `/settings/tokens`. Inference accounts reach tokens alone.

## General

Host label Text 64. Appearance: theme Segmented (light, dark, system), transitions Switch, reset Switch, remembered in the browser. Daemon rows read only: version, listener, home.

## Sources

```
 Sources                                        [Add source]
 ●  huggingface    Hugging Face     token set      [Edit]  ···
 ●  ollama         Ollama                          [Edit]  ···
 ✗  civitai        Civitai          token missing  [Edit]  ···
 ●  disk           Local            /data/models   [Edit]  ···
```

Rows: status dot, id (24ch), provider, one status word or the endpoint (40ch), Edit, menu: Delete on unseeded. Tooltip on the dot: the error. Tooltip on the provider: capabilities.

**Editor.** Provider Select on add. Then the provider's `ConfigField` list, each by type. Transport Select when several.

## Tools

```
 Tool servers                                   [Add tool server]
 ●  search    web search    searxng      1 tool     [on ●]  ···
 ✗  fs        stdio         npx          0 tools    [on ●]  ···
 ●  gh        stdio         lab-02       9 tools    [lock]
```

Rows: state dot, name (24ch), kind, one summary word (provider, command name, host, or member), tool count, enabled Switch or a lock for a member's server, menu: Edit, Delete, Try. Tooltip on the dot: the error. Expand: tools as rows with name, read only mark, description (64ch), schema in a tooltip.

**Try.** A dialog: tool Select, arguments Code json seeded from the schema, Run. Result: parts, duration, mark.

**Editor.** Kind Select, then that kind's fields. Name Text 64, enabled Switch, allowed tools Chips.

## Accounts

Admin only. Hidden with one line when sso is set or auth is off. Rows: username (24ch), role pill, created, menu: Set password, Delete. Add: username Text 64, password Secret, role Segmented.

## API tokens

Rows: name (24ch), prefix, scopes as pills, last used, menu: Revoke. Create: name Text 64, scopes Checklist limited to the caller's role. The secret shows once in a dialog with Copy. Change my password under the list: current Secret, new Secret.

## Fields

| Field | Component | Source |
| --- | --- | --- |
| `ConfigField` STRING, URL | Text | placeholder, max 256 |
| `ConfigField` PATH | Path | |
| `ConfigField` ENV | Secret | variable name as placeholder |
| `ConfigField` BOOL | Switch | |
| `ConfigField` INT | Number | |
| `ConfigField` with choices | Select | labels, disabled with reason |
| transport | Select | provider transports |
| tool kind | Select | search, fetch, stdio, http |
| provider | Segmented | searxng, brave |
| searxng_url, url | Text | url |
| brave_key | Secret | |
| command | Text | 256 |
| args | Chips | |
| env, headers | KeyValues | values as Secret |
| allowed_tools | Chips | |
| local | Switch | the Server section's `(i)` names the risk |
| role | Segmented | admin, operator, inference |
| scopes | Checklist | inference, read, operate, admin |

## Widths

Ultrawide: Split with editors right. Standard: drawers. Mobile: the summary column moves into the expand.

## Data

`GetSettings`, `UpdateSettings`, `ListSources`, `ListProviders`, `CreateSource`, `UpdateSource`, `DeleteSource`, `ListToolServers`, `CallTool`, `ListUsers`, `CreateUser`, `DeleteUser`, `SetPassword`, `ListTokens`, `CreateToken`, `DeleteToken`. Stream: SETTINGS, SOURCE, TOOL_SERVER.
