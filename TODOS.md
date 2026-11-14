# UX issues

From the staged diff and the captures in `screens/`. H1 to H10 are Nielsen's ten heuristics. ✎ marks an item circled in the annotated `Screenshot_*` files.

## 1. Broken at a viewport

- [ ] **Mobile repository title renders one character per line over the page.** `mobile-discover-repository`, `mobile-discover-repository-files`, both themes. The origin-link title at `TopBar.svelte:52` lacks the `ellipsis` class line 54 has. *WCAG 1.4.10*
- [ ] **Mobile bottom nav scrolls off-screen on any long page.** `.bottom` in `BottomBar.svelte` is neither fixed nor sticky. Host, Tasks and Discover on mobile show no nav. *Apple HIG tab bars, Material navigation bar, NN/g hidden navigation*
- [ ] **Ultrawide Chat is captured as a blank white frame in both themes.** `ultrawide-chat.png` and `dark-ultrawide-chat.png` are single-colour images, and no chat-conversations or chat-settings capture exists at 1920. Confirm the column branch at `Chat.svelte:176` mounts. *H1*
- [ ] **Host Storage row overlaps itself at 1440.** The "store" swatch covers "608 / 914 GB" and the headers read "FilesystemUsed". On mobile the table spills past the card and "Holds" is clipped to "H". *WCAG 1.4.10*
- [ ] **Mobile Accounts table spills past its card.** The inline Role select takes the width, so the username breaks as "screens / hots- / bf0487 / 0d". *WCAG 1.4.10*
- [ ] **Account menu shows a teal smear.** The translucent popover lets the "New token" button underneath bleed through between "admin" and "Sign out". *Gestalt figure and ground*
- [ ] **Gateway summary strip hides the port and orphans a cell.** "127.0.0.1:…" at 1440 and mobile, while "Timeout" wraps alone onto a second row with a 17px indent. *H1, Gestalt alignment*
- [ ] **Mobile "Balance" segmented control wraps to two rows.** "Free capacity" sits alone and centred. `mobile-gateway-route`. *Gestalt common region*
- [ ] **Mobile sheets open with focus parked on Close, drawn boxed and red.** Scheduler and task panels. On the missing-model panel the ring lands around the title. *H4*
- [ ] **Log wells wrap without a hanging indent.** Continuation lines start at column 0 and break mid-token on mobile. The task log's first line is clipped through its glyphs. *Gestalt continuity*

## 2. Staged change: sliders, placeholders, parameter form

- [ ] **Unset optional sliders show no value at all.** Chat "Temperature" and "Top p", and the pool editor's "Context length". `Slider` renders an empty reading when `optional` has no `placeholder` (`SettingsPanel.svelte:84-85`, `PoolEditor.svelte:119`). *NN/g Slider Design: Rules of Thumb, H1*
- [ ] **"Unset" is drawn as the minimum.** The docked knob on "Context length auto" matches a real minimum ("CPU 0.0 GB" in the slot editor). The hatched stub on "Slot reuse similarity 0.10" is a second unexplained encoding. *Norman mapping and signifiers, H6*
- [ ] ✎ **The -1 sentinel sits on the slider scale, below zero.** "Parallel sequences" reads "-1 sequences". For Generation limit and Reasoning budget, -1 is unlimited and 0 ends thinking at once, so dragging towards less lands on unlimited (`llamacpp.go` `States`). *Norman natural mapping, H2*
- [ ] **Sliders are used for values that must be exact.** Context length, Fit context floor and batch sizes on an unlabelled 256 to 1,048,576 log rail with no ticks or end labels. *NN/g Slider Design*
- [ ] **The slider type-in is hidden, and typed values can leave the rail.** The reading looks like static text (`tabindex=-1`, a well only on hover). `commit()` accepts numbers past the ends, so the knob pins at max while the reading says more. *H6, WCAG 4.1.2*
- [ ] ✎ **Built-in defaults as placeholder text make empty fields look filled.** "f16", "auto", "8192 MiB", "the launch's name", "same as threads". On the Defaults tab the grey default sits inside the field that sets the default. Grey measures 2.79:1 in light, and the unit is darker than the value. *NN/g Placeholders in Form Fields Are Harmful, WCAG 1.4.3*
- [ ] ✎ **On/off settings use two different controls in one group.** Tri-state ones ("Unified cache pool", "Context shift") are selects showing a grey "on", "off" or "auto". Plain booleans are switches (`switchParam` vs `boolParam`). *H4, NN/g Toggle-Switch Guidelines*
- [ ] ✎ **Three disclosure idioms in one form.** "Advanced ⌄" on the right of some groups (about 675px from its heading at 1920), "› Batching" caret rows for others, and Launch's outer "Parameters" fold. Every button is named just "Advanced", and opened fields join the same grid with no boundary. *H4, NN/g Progressive Disclosure, WCAG 2.4.6*
- [ ] ✎ **Parameter group headings sit at the same level as the editor's own sections.** In the slot editor "Context / Placement / Cache" read as siblings of "Memory / Limits / Tools / Aliases". *WCAG 1.3.1*
- [ ] ✎ **The auto-fill grid strands fields.** "Slot reuse similarity" sits alone on a row at 1440, Placement's third column is empty at 1920, and the form runs off-screen beside about 680px of empty list card. *Gestalt proximity*
- [ ] **Help lives only in hover tooltips.** Dotted-underline labels open on hover or focus of a non-focusable span (`Tip.svelte`, `FieldLabel.svelte`), unreachable by touch and keyboard. The diff adds "empty takes …" there. The underline is uneven ("Value cache type" yes, "Key cache type" no). *NN/g Tooltip Guidelines, WCAG 1.4.13, 2.1.1*
- [ ] **Empty-state buttons are bare nouns.** "Route", "Pool" and "Mesh" beside a "+ Route" header button, three create affordances with the editor open. Traffic and Chat still offer "Launch". *NN/g Designing Empty States in Complex Applications*
- [ ] **Slot "Memory" sliders don't say what the number is.** The label is only the device name. A new slot defaults to the whole device (12.0 GB while 6.8 is in use, CPU 62.5 against a 62.6 total), an existing one shows CPU 0.0. *H2, H5*
- [ ] **`nebu runtimes show` prints prose under DEFAULT.** "the model's own GPU" appears beside literal defaults (`internal/cli/runtimes.go`). *H4*

## 3. Serve and runtimes

- [ ] ✎ **Launch opens a 1080px modal to say "No models stored".** Disabled "Launch", no reason given, every entry point still offers it. *H5, NN/g Modal and Nonmodal Dialogs*
- [ ] **Primary "Launch" drops under the filter.** With a panel open and on mobile. "Filter" is a select whose only label is its placeholder. *Gestalt proximity, WCAG 3.3.2*
- [ ] **An empty slot row is a hollow ring and a name.** No state word, blank Model cell. The panel header "empty · asdasdasd" repeats the description field below it. *H1, H8*
- [ ] **Uninstalled runtimes wear the same green as the installed one.** Only fill versus ring on a 6px dot differs, and Installs is blank instead of 0. *WCAG 1.4.1, Gestalt similarity*
- [ ] **Install panel mixes the form with existing installs.** Fields, then the "v0.9.0 … Remove" row, then "Cancel / Add". "Add" is enabled with an empty Binary while every other create button disables. *Gestalt common region, H4*
- [ ] ✎ **Install and build details are an unordered key dump.** Capability flags mixed with identity facts, "Accepted accepted", "Ready binary present", raw cmake flags, "GenuineIntel,nvidia", the same instance listed three times. *H2, H8*
- [ ] ✎ **Unit pickers offer units that cannot apply.** "Time per output token" offers ms, s, min, h and defaults to s. "Speed limit per second" offers MB, GB, TB with no KB. Request timeout goes up to days. *H5, Hick's law*

## 4. Models and Discover

- [ ] **Store bar is two-thirds full for a 36.8 KB store.** The fill is whole-disk usage with no legend, and Host reports the same store as 64.0 KB. *Tufte graphical integrity, H4*
- [ ] **Missing-model panel is a dead end.** "Model failed to load" over the raw key "model not stored: huggingface/…/default". The only button is "Back", a duplicate of ×, with no pull action. *H9*
- [ ] **Discover columns mix attributes.** "Size" holds "9.4B params", "10.9 GB" and blanks. "Details" runs format, library and licence together, down to a bare "other". Fit appears only inside a repo, and mobile drops both columns. *H2, H4*
- [ ] ✎ **Weights table "Context" column holds "No fit".** Pill colour encodes memory tier, the legend shows rings that never appear in the table, and rows are named "model" and "model-mtp". *H2, WCAG 1.4.1*
- [ ] ✎ **Revision input "main" has no label.** It reads as a search box. *WCAG 3.3.2*
- [ ] **"Filters" looks identical open and closed.** `aria-expanded` only, no count. The filters are placeholder-only, and on mobile six of them push the first result to about y=590 of 844. *H1, NN/g Placeholders*
- [ ] **Ultrawide repository page is lopsided.** README lines run about 98 characters, the left column has about 760px empty, and the two column tops sit 20px apart. *WCAG 1.4.8, Bringhurst*

## 5. Chat

- [ ] **Deleting and Rolling-back impossible** Any given chat message should be deletable, editable (in-place fork, do not create new message), retry (same as edit, without change to any content, just regen), deletable (removes message from "chain" of context, doesnt regenerate anything just a removal). Will require some server-side implementation and a rpc.
- [ ] **Chat UI is very uninspired** The morphic style of neo should be preserved or even accentuated, but should also work to improve and potentially redesign the UI.
- [ ] **Lacking "Theater" mode** There should be a theater mode, which works like a nested fullscreen mode within the window, similar to youtube's. For full chat immersion.  
- [ ] **Picker says "Model", empty state says "No ready route" and "Launch".** *H2, H4*
- [ ] **Conversations trigger is top-left but its drawer opens from the right.** A search box sits over "No conversations", and the settings drawer blurs the chat it tunes. *Fitts's law, NN/g empty states*
- [ ] **"User" and "Assistant" fields are unexplained.** "User" is truncated to "screenshots-bf04…", and label-above and label-left rows are mixed. *H2, Gestalt alignment*

## 6. Gateway and Traffic

- [ ] **Summary strip styles a live count like the limits.** "Requests 0" beside "Concurrent requests ∞" and a unitless "Rate ∞". "Requests" is a count here and "/min" on Traffic. "plain" is amber with no explanation. *H2, H4*
- [ ] **One metric has three names.** "TTFT" in the table, "Time to first token" in the form, "First token" in the stat, with no statistic named. *H4, H6*
- [ ] **Pool detail hides its failures.** "no ready endpoints" and "scrape failing" use the same grey ring as the benign "not exported", while the list shows a red dot. The endpoint row has unexplained green and red dots. *H1, H9*
- [ ] **Editor and read view use different words.** "Reject / Let through" becomes "fail close", "HTTP" becomes "http", "State loaded" becomes "Status Accepted accepted". *H4*
- [ ] **Pool editor separates controls from labels.** The "Selector" label's "+" is about 730px away. Model and Accelerator are free text beside picker fields. The Export toggle sits on the label line. The off "Send token ids" toggle measures 1.23:1. *Fitts's law, WCAG 1.4.11*
- [ ] **Route editor has two identical × in one column.** One removes the rule, one removes the target. A lone rule still has a drag handle, "Weight" shows "1 100%" in one field, and "Match" gives no syntax example. *Gestalt similarity, H5*
- [ ] **Schedulers list carries no information.** Every row is "loaded" with a green dot, "default" has no row menu, "pd" has no description, and the detail shows 40 lines of YAML before status. *H8, H2*
- [ ] **Traffic filters are split and the empty views are identical.** Four filters inline, two behind "Filters". At 1920 the popover covers "No requests" and leaves "Launch" directly beneath it. Empty Timeline matches List pixel for pixel, and mobile has no Timeline. *Gestalt proximity, H1*
- [ ] **Mobile sub-navigation differs by section.** Gateway, Settings and bot pages use a select (reading "Status" on a bot page), while Host, Serve and Models keep tabs. *H4, NN/g hidden navigation*

## 7. Host and Tasks

- [ ] **"Serving" card is a bare "—".** "First token —" has no unit, and System lists raw probe names ("drm-sysfs", "checkpoint") with an unexplained ⊘. *H2, NN/g empty states*
- [ ] **Finished task log still offers "Follow" and "1000 lines".** "warn" lines are the same green as "ok" (both 159,211,154), "Result" is a link reading "Host", and "×13" opens one run without saying which. *H1, H9*
- [ ] **Task list chevrons are inconsistent.** Only on grouped rows when wide, on every row with a panel open. Also "Verify 1 models", and mobile drops "Finished" but keeps "Duration". *H4*

## 8. Bots and Mesh

- [ ] **Bots "Servers" column holds only Start buttons.** State is shown three ways on desktop and by a ring alone on mobile. *H2, WCAG 1.4.1*
- [ ] **Bot page "Not connected" has no action.** "Start" lives in a "Details" card holding two identical timestamps. *NN/g empty states, Fitts's law*
- [ ] **"Check" and "Send test" are unstyled grey text in or beside the field.** They read as placeholder and look active with nothing entered. "Start it now" floats mid-row. *Norman signifiers*
- [ ] **Mesh "This node" shows nothing about the node.** Only "Heard nearby 0", and the next card repeats "Nothing nearby" with no explanation or action. *H8, H2*
- [ ] **Join dialog doesn't say where a token comes from.** The token is a plain text field while the bot token is masked. *H10, H4*

## 9. Settings and Profile

- [ ] **General's first and largest card is the logo.** Its only control is an unlabelled GitHub icon, and it pushes Host and Log level down. *H8, NN/g Icon Usability*
- [ ] **Empty fields don't say what empty means.** "Speed limit" and "Store limit" are blank with a unit preselected, "Trusted proxies" and "Allowed tools" could mean all or none, and "disco" in Host label is placeholder grey. *H1, H5*
- [ ] **Role changes instantly from an inline select, including your own row.** `setRole` has no confirm and ends that account's sessions (`Accounts.svelte:103`). The dialog uses a segmented control instead, and Profile shows "admin" lower-case. *H5, H4*
- [ ] **Sources status mixes "token set", "ready" and "token missing".** On mobile and with a panel open it collapses to a green or orange dot differing by 1.07:1 in luminance. Row chevrons appear only sometimes. *WCAG 1.4.1, H4*
- [ ] **Save controls exist only once a form is dirty.** The Password card is two fields and no button, and the source editor has no footer, while the slot editor always shows Delete, Discard and Save. *H4, H6*
- [ ] **Editor chrome and scopes are misplaced or unexplained.** "Enabled" sits in the title bar 25px from ×, "SearXNG URL" isn't marked required, the "Set password" username looks editable, and token scopes are unexplained checkboxes. *Fitts's law, WCAG 3.3.2*
- [ ] **Duplicate and inconsistently named create actions.** "New token" and "Add webhook" each appear twice on one card, and creation is called "+ Slot", "Add account", "New token" or "Create a mesh". *H4, H8*

## 10. Systemic, measured from the captures

- [ ] ✎ **Control boundaries are far below 3:1 in both themes.** *WCAG 1.4.11*

  | Element | Light | Dark |
  |---|---|---|
  | Input edge | 1.21 | 1.10 |
  | Unchecked checkbox | 1.22 | 1.10 |
  | Off switch | 1.23 | 1.23 |
  | Outlined button border | 1.61 | 1.70 |
  | Dialog interior vs backdrop, no scrim | 1.00 | 1.00 |
  | Log well vs card | 16 | 1.05 |

- [ ] **Segmented selection is text hue alone.** In dark the selected option is dimmer (8.85:1) than the unselected ones (15.17:1). In light it is 4.16:1, and 3.9:1 in unit chips, which are about 17×28px on mobile. *WCAG 1.4.1, 1.4.3, 2.5.8*
- [ ] **Ultrawide stretches everything.** Tables put about 1500px between a name and its value or action, "1" sits in a 590px field, and steppers are 250 to 300px from their number. *Baymard on field width, Fitts's law*
- [ ] **Disabled Create, Save and Launch buttons never say what is missing.** *Smashing, Usability Pitfalls of Disabled Buttons*
