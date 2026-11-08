# Gateway

Routes, pools, objectives, scheduler configs, imports.

Route: `/gateway/routes`, `/gateway/pools`, `/gateway/objectives`, `/gateway/schedulers`, `/gateway/imports`. An id after the tab opens its panel.

## Strip

```
 Listener  Transport  Auth  Requests  In flight cap  Rate  Timeout  Version
 :8484     TLS        on    1.2M      ∞              ∞     ∞        nebu 0.9.3
```

A facts card over every tab, the fallback policy read as literal values or an infinity. Tooltips: every listener, burst, upstream timeout, metrics address. The New button of each tab sits in its card's tools.

## Routes

```
 ●  chat          2 pools             4 in flight    [Edit] ···
 ●  llama-70b     llama-70b-h100      3 in flight    [Open slot]
 ◐  qwen-72b      qwen-72b-h100       pending        [Open slot]
 ●  deepseek      forwarded lab-02    1 in flight    [Open formation]
```

Rows: state dot, name (24ch), target phrase (24ch: the pool, n pools, or forwarded node), in flight, one action by owner, menu: Delete for user routes. Tooltip on the name: owner, context, quantization, accelerator, modalities. Expand: rules as a Stack, each rule a line with its match then one indented line per target with weight percent, model override and pool state. Under them: objective, balance, tools, conditions.

**Editor.** Name Text 64 locked when editing. Rules as a sortable list, each with match Text and target rows: pool Picker, model Text 64, weight Weight. Policy as in Serve. Tools Checklist, mutating Switch, objective Select, balance Segmented.

## Pools

```
 ●  llama-70b-h100    2 / 2 ready    Llama-3.3-70B        [Open]  ···
 ●  qwen-72b-h100     0 / 1 ready    Qwen2.5-72B-AWQ      [Open]  ···
```

Rows: dot, id (24ch), ready over total, model (40ch), Open, menu: Edit and Delete for user pools. Tooltip: runtime, accelerator, owner, export, protocol, failure mode, scheduler config.

**Panel.** Endpoints as rows: address (24ch), ready and healthy dots, queued, running, KV bar. Tooltip: node, instance, role, rank, assumed, adapters, predicted TTFT and TPOT, metrics age. Conditions. Spec under a collapse.

**Editor.** Id Text 64, model Text 128, runtime Select, accelerator Text 64, selector KeyValues, target ports Chips of Number 1 to 65535 up to eight, protocol Segmented, failure mode Segmented, scheduler config Select, export Switch, concurrency Number, tokens in and out Switch, static endpoints rows: address Text, role Text, rank Number, kv capacity Number. Inventory and metric names under Advanced.

## Objectives

Rows: name (24ch), priority, TTFT, TPOT, menu: Edit, Delete. Editor: name Text 64, priority Slider -1000 to 1000, TTFT Duration, TPOT Duration, an empty duration setting no target.

## Schedulers

Rows: name, conditions dot, referenced by n pools, Open. Panel: Code yaml filling the panel, Validate, Save. Delete only when unreferenced. New starts from a commented skeleton.

## Imports

Rows: id, routing mode pill, ready endpoints over total. Expand: controllers with their sources and conditions, endpoints. Read only.

## Widths

Ultrawide: Split. Standard: drawer. Mobile: the target phrase and in flight move into the expand.

## Data

`GetGatewayStatus`, `PutRoute`, `DeleteRoute`, `PutPool`, `DeletePool`, `PutObjective`, `DeleteObjective`, `PutSchedulerConfig`, `DeleteSchedulerConfig`, `ListPoolImports`, `WatchPoolImports`, `ListPools` for the pool Picker, `ListToolServers`. Stream: ROUTE, POOL, OBJECTIVE, SCHEDULER_CONFIG.
