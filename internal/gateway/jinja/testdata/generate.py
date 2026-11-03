#!/usr/bin/env python3
# Writes fixtures and golden outputs rendered by transformers' chat template compiler under a fixed clock
import copy
import datetime
import functools
import gzip
import inspect
import json
import os
import pprint
import random
import subprocess
import sys
import types
from collections import Counter, deque

import jinja2
import jinja2.compiler
import jinja2.environment
import jinja2.nodes
import jinja2.runtime
import jinja2.sandbox
import markupsafe
from jinja2 import nodes
from jinja2.utils import _PassArg
from transformers.utils import chat_template_utils

HERE = os.path.dirname(os.path.abspath(__file__))
FIXED_NOW = datetime.datetime(2026, 11, 2, 9, 5, 7, 123456)


class FixedDatetime(datetime.datetime):
    # The clock strftime_now reads inside transformers
    @classmethod
    def now(cls, tz=None):
        return FIXED_NOW


chat_template_utils.datetime = FixedDatetime


def compile_template(source):
    # The exact compiled template transformers' apply_chat_template renders
    return chat_template_utils._compile_jinja_template(source)


def _is_var_access(node, varname):
    if isinstance(node, jinja2.nodes.Name):
        return node.ctx == "load" and node.name == varname
    if isinstance(node, jinja2.nodes.Getattr) and "." in varname:
        base, _, attr = varname.rpartition(".")
        return node.attr == attr and _is_var_access(node.node, base)
    return False


def _is_attr_access(node, varname, key):
    if isinstance(node, jinja2.nodes.Getitem):
        return _is_var_access(node.node, varname) and isinstance(node.arg, jinja2.nodes.Const) and node.arg.value == key
    if isinstance(node, jinja2.nodes.Getattr):
        return _is_var_access(node.node, varname) and node.attr == key
    return False


def _is_var_or_elems_access(node, varname, key=None):
    if isinstance(node, jinja2.nodes.Filter):
        return node.node is not None and _is_var_or_elems_access(node.node, varname, key)
    if isinstance(node, jinja2.nodes.Test):
        return _is_var_or_elems_access(node.node, varname, key)
    if isinstance(node, jinja2.nodes.Getitem) and isinstance(node.arg, jinja2.nodes.Slice):
        return _is_var_or_elems_access(node.node, varname, key)
    return _is_attr_access(node, varname, key) if key else _is_var_access(node, varname)


def _attr_dotted_name(node):
    if isinstance(node, jinja2.nodes.NSRef):
        return f"{node.name}.{node.attr}"
    parts = []
    while isinstance(node, jinja2.nodes.Getattr):
        parts.append(node.attr)
        node = node.node
    if isinstance(node, jinja2.nodes.Name):
        parts.append(node.name)
        return ".".join(reversed(parts))
    return None


def _iter_nodes_assign_var_or_elems(root, varname):
    yield root, varname
    seen = {varname}
    related_varnames = deque([varname])
    while related_varnames:
        related_varname = related_varnames.popleft()
        for assign_ast in root.find_all(jinja2.nodes.Assign):
            lhs = assign_ast.target
            rhs = assign_ast.node
            if isinstance(lhs, jinja2.nodes.Name) and _is_var_or_elems_access(rhs, related_varname):
                yield assign_ast, lhs.name
                if lhs.name != related_varname and lhs.name not in seen:
                    seen.add(lhs.name)
                    related_varnames.append(lhs.name)
            aliases = []
            if isinstance(lhs, jinja2.nodes.Name) and isinstance(rhs, jinja2.nodes.Call) and isinstance(rhs.node, jinja2.nodes.Name) and rhs.node.name == "namespace":
                aliases = [f"{lhs.name}.{kwarg.key}" for kwarg in rhs.kwargs if _is_var_or_elems_access(kwarg.value, related_varname)]
            elif isinstance(lhs, (jinja2.nodes.Getattr, jinja2.nodes.NSRef)) and _is_var_or_elems_access(rhs, related_varname):
                dotted = _attr_dotted_name(lhs)
                aliases = [dotted] if dotted is not None else []
            for alias in aliases:
                yield assign_ast, alias
                if alias != related_varname and alias not in seen:
                    seen.add(alias)
                    related_varnames.append(alias)


def _iter_nodes_assign_messages_item(root):
    messages_varnames = [varname for _, varname in _iter_nodes_assign_var_or_elems(root, "messages")]
    for loop_ast in root.find_all(jinja2.nodes.For):
        for varname in messages_varnames:
            if _is_var_or_elems_access(loop_ast.iter, varname):
                assert isinstance(loop_ast.target, jinja2.nodes.Name)
                yield loop_ast, loop_ast.target.name
                break


def _iter_nodes_assign_content_item(root):
    message_varnames = [varname for _, varname in _iter_nodes_assign_messages_item(root)]
    macro_content_params_by_loop = {}
    loops_in_macros = set()
    for macro_node in root.find_all(jinja2.nodes.Macro):
        macro_param_names = {arg.name for arg in macro_node.args}
        macro_content_params = set()
        for call_node in root.find_all(jinja2.nodes.Call):
            if isinstance(call_node.node, jinja2.nodes.Name) and call_node.node.name == macro_node.name:
                for i, arg in enumerate(call_node.args):
                    if i < len(macro_node.args) and any(_is_var_or_elems_access(arg, v, "content") for v in message_varnames):
                        macro_content_params.add(macro_node.args[i].name)
                for kwarg in call_node.kwargs:
                    if kwarg.key in macro_param_names and any(_is_var_or_elems_access(kwarg.value, v, "content") for v in message_varnames):
                        macro_content_params.add(kwarg.key)
        for loop_ast in macro_node.find_all(jinja2.nodes.For):
            loops_in_macros.add(id(loop_ast))
            if macro_content_params:
                macro_content_params_by_loop[id(loop_ast)] = macro_content_params
    for loop_ast in root.find_all(jinja2.nodes.For):
        loop_iter = loop_ast.iter
        for varname in message_varnames:
            if _is_var_or_elems_access(loop_iter, varname, "content"):
                assert isinstance(loop_ast.target, jinja2.nodes.Name)
                yield loop_ast, loop_ast.target.name
                break
        else:
            params = macro_content_params_by_loop.get(id(loop_ast))
            if isinstance(loop_iter, jinja2.nodes.Name) and params is not None and loop_iter.name in params:
                assert isinstance(loop_ast.target, jinja2.nodes.Name)
                yield loop_ast, loop_ast.target.name
                continue
            if id(loop_ast) not in loops_in_macros and isinstance(loop_iter, jinja2.nodes.Name) and loop_iter.name == "content":
                assert isinstance(loop_ast.target, jinja2.nodes.Name)
                yield loop_ast, loop_ast.target.name


def detect_content_format(tmpl, source):
    # vLLM's _detect_content_format over the template transformers parses
    try:
        next(_iter_nodes_assign_content_item(tmpl.environment.parse(source)))
    except StopIteration:
        return "string"
    except Exception:
        return "string"
    return "openai"


def strip_billing_header(parts):
    out = []
    for part in parts:
        text = part.get("text")
        if not (isinstance(text, str) and text.startswith("x-anthropic-billing-header")):
            out.append(part)
            continue
        rest = text.partition("\n")[2]
        if rest:
            out.append({**part, "text": rest})
    return out


def vllm_conversation(messages, content_format, source):
    # The conversation vLLM's parse_chat_messages and safe_apply_chat_template hand transformers
    conversation = []
    for msg in messages:
        role = msg["role"]
        content = msg.get("content")
        parts = [] if content is None else [{"type": "text", "text": content}] if isinstance(content, str) else content
        if role == "system":
            parts = strip_billing_header(parts)
        wrap = content_format == "openai"
        items = []
        for p in parts:
            res = {"type": "text", "text": p["text"]} if wrap else p["text"]
            if res:
                items.append(res)
        out = {"role": role, "content": items if wrap else "\n".join(items)}
        if role == "assistant":
            if msg.get("tool_calls") is not None:
                out["tool_calls"] = [
                    {"id": tc["id"], "function": {"arguments": tc["function"]["arguments"], "name": tc["function"]["name"]}, "type": tc.get("type", "function")}
                    for tc in msg["tool_calls"]
                ]
        elif role == "tool":
            if "tool_call_id" in msg:
                out["tool_call_id"] = msg["tool_call_id"]
            if isinstance(out["content"], list):
                texts = [i.get("text", "") for i in out["content"] if isinstance(i, dict) and i.get("type") == "text"]
                out["content"] = "\n".join(texts) if texts else ""
        if isinstance(msg.get("name"), str):
            out["name"] = msg["name"]
        if role == "developer":
            out["tools"] = msg.get("tools", None)
        conversation.append(out)
    for m in conversation:
        if m["role"] == "assistant" and "tool_calls" in m:
            if len(m["tool_calls"]) == 0:
                m.pop("tool_calls")
                continue
            for item in m["tool_calls"]:
                fn = item["function"]
                if arguments := fn.get("arguments"):
                    parsed = json.loads(arguments)
                    if not isinstance(parsed, dict):
                        raise ValueError("arguments are not a JSON object")
                    fn["arguments"] = parsed
                else:
                    fn["arguments"] = {}
    supports_developer = '"developer"' in source or "'developer'" in source
    if any(m["role"] == "developer" for m in conversation) and not supports_developer:
        converted = []
        for m in conversation:
            if m["role"] == "developer":
                m = dict(m)
                m["role"] = "system"
                m.pop("tools", None)
            converted.append(m)
        conversation = consolidate_system(converted)
    return conversation


def consolidate_system(conversation):
    contents, rest, needed = [], [], False
    for i, msg in enumerate(conversation):
        if msg["role"] != "system":
            rest.append(msg)
            continue
        if i > 0 or contents:
            needed = True
        content = msg.get("content", "")
        if isinstance(content, list):
            content = "\n".join(p["text"] for p in content if isinstance(p, dict) and "text" in p)
        if content:
            contents.append(content)
    if not needed:
        return conversation
    return [{"role": "system", "content": "\n\n".join(contents)}, *rest]


def tool_dicts(tools):
    if not tools:
        return None
    return [
        {"type": "function", "function": {"name": t["function"]["name"], "description": t["function"].get("description"), "parameters": t["function"].get("parameters")}}
        for t in tools
    ]


SYS = {"role": "system", "content": "You are a helpful assistant."}
WEATHER = {"type": "function", "function": {"name": "get_weather", "description": "Get the current weather for a city", "parameters": {"type": "object", "properties": {"city": {"type": "string", "description": "City name, e.g. Paris"}, "unit": {"type": "string", "enum": ["celsius", "fahrenheit"], "description": "Temperature unit"}}, "required": ["city"]}}}
SEARCH = {"type": "function", "function": {"name": "search_docs", "description": "Search documentation pages", "parameters": {"type": "object", "properties": {"query": {"type": "string", "description": "Search terms"}, "limit": {"type": "integer", "description": "Maximum results", "minimum": 1, "maximum": 50, "default": 10}, "tags": {"type": "array", "items": {"type": "string"}, "description": "Filter tags"}, "score": {"type": "number", "description": "Minimum score 0.5"}, "exact": {"type": "boolean", "description": "Exact match"}, "filters": {"type": "object", "description": "Field filters", "properties": {"lang": {"type": "string"}}}}, "required": ["query", "limit"]}}}
PING = {"type": "function", "function": {"name": "ping", "parameters": {"type": "object", "properties": {}}}}
TOOLS = [WEATHER, SEARCH]
PINGS = [PING]
CALL1 = {"id": "call00001", "type": "function", "function": {"name": "get_weather", "arguments": "{\"city\": \"Paris\", \"unit\": \"celsius\"}"}}
CALL2 = {"id": "call00002", "type": "function", "function": {"name": "get_weather", "arguments": "{\"city\": \"Berlin\"}"}}
NESTED = {"id": "call00004", "type": "function", "function": {"name": "search_docs", "arguments": json.dumps({"query": "jinja \"templates\" é", "limit": 5, "tags": ["a", "b"], "score": 0.75, "exact": False, "extra": None, "filters": {"lang": "go", "deep": [1, 2.5, {"z": "ü"}]}})}}
ASK = {"role": "user", "content": "What's the weather in Paris?"}
RESULT1 = {"role": "tool", "tool_call_id": "call00001", "content": "{\"temperature\": 22, \"condition\": \"sunny\"}"}
RESULT2 = {"role": "tool", "tool_call_id": "call00002", "content": "{\"temperature\": 15}"}

CONVERSATIONS = {
    "user_only": [{"role": "user", "content": "Hello!"}],
    "sys_user": [SYS, {"role": "user", "content": "What is the capital of France?"}],
    "multi_turn": [{"role": "user", "content": "Hi there!"}, {"role": "assistant", "content": "Hello! How can I help you today?"}, {"role": "user", "content": "Tell me a joke about cats."}],
    "sys_multi": [SYS, {"role": "user", "content": "Hi there!"}, {"role": "assistant", "content": "  Hello! How can I help?  "}, {"role": "user", "content": "Name three colors."}],
    "assistant_last": [SYS, {"role": "user", "content": "Count to three."}, {"role": "assistant", "content": "One, two, three."}],
    "list_content": [{"role": "user", "content": [{"type": "text", "text": "Describe this:"}, {"type": "text", "text": "a red apple"}]}, {"role": "assistant", "content": [{"type": "text", "text": "It is red."}]}, {"role": "user", "content": "Thanks"}],
    "system_list": [{"role": "system", "content": [{"type": "text", "text": "Be brief."}, {"type": "text", "text": "Use metric units."}]}, {"role": "user", "content": "How tall is Everest?"}],
    "unicode": [SYS, {"role": "user", "content": "Café ☕ — naïve 日本語 \"quotes\" and 'single' \\ backslash\nnew line\ttab </s> {{ not_a_tag }} {% raw %} 🙂"}],
    "whitespace": [{"role": "system", "content": "\n  Padded system.  \n"}, {"role": "user", "content": "  spaced question?  \n\n"}],
    "empty_user": [{"role": "user", "content": ""}],
    "system_only": [SYS],
    "consecutive_users": [{"role": "user", "content": "First."}, {"role": "user", "content": "Second."}],
    "tool_call": [SYS, ASK, {"role": "assistant", "content": None, "tool_calls": [CALL1]}, RESULT1],
    "tool_call_answer": [SYS, ASK, {"role": "assistant", "content": None, "tool_calls": [CALL1]}, RESULT1, {"role": "assistant", "content": "It is 22°C and sunny in Paris."}, {"role": "user", "content": "And in Berlin?"}],
    "multi_tool_calls": [ASK, {"role": "assistant", "content": "Let me check two cities.", "tool_calls": [CALL1, CALL2]}, RESULT1, RESULT2],
    "nested_args": [{"role": "user", "content": "Find docs."}, {"role": "assistant", "content": None, "tool_calls": [NESTED]}, {"role": "tool", "tool_call_id": "call00004", "content": "[]"}],
    "empty_args": [ASK, {"role": "assistant", "content": None, "tool_calls": [{"id": "call00003", "type": "function", "function": {"name": "ping", "arguments": ""}}]}, {"role": "tool", "tool_call_id": "call00003", "content": "pong"}],
    "tool_list_result": [ASK, {"role": "assistant", "content": None, "tool_calls": [CALL1]}, {"role": "tool", "tool_call_id": "call00001", "content": [{"type": "text", "text": "22C"}, {"type": "text", "text": "sunny"}]}],
    "reasoning": [{"role": "user", "content": "What is 2+2?"}, {"role": "assistant", "content": "<think>\nSimple arithmetic.\n</think>\n\nThe answer is 4."}, {"role": "user", "content": "And 3+3?"}],
    "developer": [{"role": "developer", "content": "Answer tersely."}, {"role": "user", "content": "Why is the sky blue?"}],
    "dev_after_sys": [SYS, {"role": "user", "content": "Hi"}, {"role": "developer", "content": "Switch to French."}, {"role": "user", "content": "Bonjour?"}],
    "billing": [{"role": "system", "content": "x-anthropic-billing-header: cch=abc123\nYou are a coding agent."}, {"role": "user", "content": "List files."}],
    "named": [{"role": "user", "content": "Hi, I'm Ann.", "name": "ann"}],
}

# Fixtures every template renders, as conversation, generation prompt, tools and kwargs
COMMON = [
    ("user_only", True, None, None),
    ("sys_user", True, None, None),
    ("sys_user", False, None, None),
    ("multi_turn", False, None, None),
    ("multi_turn", True, None, None),
    ("sys_multi", True, None, None),
    ("assistant_last", False, None, None),
    ("list_content", True, None, None),
    ("system_list", True, None, None),
    ("unicode", True, None, None),
    ("whitespace", True, None, None),
    ("empty_user", True, None, None),
    ("system_only", True, None, None),
    ("consecutive_users", True, None, None),
    ("sys_user", True, TOOLS, None),
    ("user_only", True, PINGS, None),
    ("tool_call", True, TOOLS, None),
    ("tool_call_answer", True, TOOLS, None),
    ("multi_tool_calls", True, TOOLS, None),
    ("nested_args", True, TOOLS, None),
    ("empty_args", True, PINGS, None),
    ("tool_list_result", True, TOOLS, None),
    ("tool_call", True, None, None),
    ("reasoning", True, None, None),
    ("developer", True, None, None),
    ("dev_after_sys", True, None, None),
    ("billing", True, None, None),
    ("named", True, None, None),
    ("sys_user", True, None, {"enable_thinking": False}),
    ("reasoning", True, None, {"enable_thinking": True}),
    ("tool_call", True, TOOLS, {"enable_thinking": False}),
    ("sys_user", True, None, {"bos_token": "<|bos|>", "eos_token": "<|eos|>"}),
]

TOOL_SETS = {"tools": TOOLS, "ping": PINGS}

DOCS = [{"title": "Paris", "text": "Paris is the capital of France."}, {"title": "Berlin", "text": "Berlin is the capital of Germany."}]

# Fixtures for template specific arguments
EXTRA = {
    "llama31": [("sys_user", True, None, {"date_string": "26 Jul 2024"}), ("sys_user", True, TOOLS, {"tools_in_user_message": False}), ("tool_call", True, TOOLS, {"builtin_tools": ["brave_search", "wolfram_alpha"]})],
    "llama32": [("sys_user", True, None, {"date_string": "26 Jul 2024"}), ("tool_call", True, TOOLS, {"tools_in_user_message": False})],
    "gptoss": [("sys_user", True, None, {"reasoning_effort": "high"}), ("tool_call", True, TOOLS, {"model_identity": "You are Nebu.", "builtin_tools": ["browser", "python"]}), ("developer", True, TOOLS, {"reasoning_effort": "low"})],
    "granite33": [("sys_user", True, None, {"thinking": True}), ("user_only", True, None, {"documents": DOCS, "controls": {"citations": True, "hallucinations": True, "length": "short"}}), ("tool_call", True, TOOLS, {"thinking": True})],
    "smollm3": [("sys_user", True, None, {"xml_tools": [WEATHER]}), ("user_only", True, None, {"python_tools": [SEARCH], "enable_thinking": False})],
    "deepseek_v31": [("sys_user", True, None, {"thinking": True}), ("tool_call_answer", True, TOOLS, {"thinking": False})],
    "seedoss": [("sys_user", True, None, {"thinking_budget": 512}), ("sys_user", True, TOOLS, {"use_json_tooldef": True}), ("sys_user", True, None, {"thinking_budget": 0})],
    "minimaxm2": [("sys_user", True, TOOLS, {"model_identity": "You are Nebu."})],
    "commandr_plus": [("sys_user", True, None, {"documents": DOCS, "citation_mode": "fast"})],
}


def case_name(template, conv, agp, tools, kwargs, n):
    name = f"{template}__{conv}"
    if not agp:
        name += "_nogen"
    if tools:
        name += f"_{tools}"
    if kwargs:
        name += f"_kw{n}"
    return name


def build_cases(corpus):
    cases = []
    for entry in corpus:
        fixtures = COMMON + EXTRA.get(entry["name"], [])
        seen = {}
        for i, (conv, agp, tools, kwargs) in enumerate(fixtures):
            tools = next((k for k, v in TOOL_SETS.items() if v is tools), None)
            name = case_name(entry["name"], conv, agp, tools, kwargs, i)
            assert name not in seen, name
            seen[name] = True
            c = {"name": name, "template": entry["template"], "conversation": conv, "add_generation_prompt": agp}
            if entry.get("tool_use"):
                c["tool_use"] = entry["tool_use"]
            if tools:
                c["tools"] = tools
            if kwargs:
                c["kwargs"] = kwargs
            cases.append(c)
    return cases


def raw_call(arguments, cid="call00001", name="get_weather"):
    return {"id": cid, "type": "function", "function": {"name": name, "arguments": arguments}}


WEATHER_ARGS = {"city": "Paris", "unit": "celsius"}
RAW_TOOL_TURNS = lambda call: [SYS, ASK, {"role": "assistant", "content": None, "tool_calls": [call]}, {"role": "tool", "tool_call_id": "call00001", "name": "get_weather", "content": "{\"temperature\": 22}"}, {"role": "assistant", "content": "It is 22 degrees."}, {"role": "user", "content": "Thanks!"}]
RAW_SPECIAL = {"bos_token": "<|bos|>", "eos_token": "<|eos|>", "unk_token": "<unk>", "pad_token": "<pad>"}


def raw_vars(messages, agp=True, tools=None, documents=None, **kwargs):
    return {**RAW_SPECIAL, "messages": messages, "tools": tools, "documents": documents, "add_generation_prompt": agp, **kwargs}


# Inputs handed to the template as transformers receives them from a caller, with no vLLM reshaping
RAW = [
    ("args_string", raw_vars(RAW_TOOL_TURNS(raw_call(json.dumps(WEATHER_ARGS))), tools=TOOLS)),
    ("args_object", raw_vars(RAW_TOOL_TURNS(raw_call(WEATHER_ARGS)), tools=TOOLS)),
    ("args_nested_object", raw_vars([{"role": "user", "content": "Find docs."}, {"role": "assistant", "content": "", "tool_calls": [raw_call(json.loads(NESTED["function"]["arguments"]), "call00004", "search_docs")]}, {"role": "tool", "tool_call_id": "call00004", "content": "[]"}], tools=TOOLS)),
    ("list_parts", raw_vars([{"role": "system", "content": [{"type": "text", "text": "Be brief."}]}, {"role": "user", "content": [{"type": "text", "text": "Describe this:"}, {"type": "text", "text": "a red apple"}]}, {"role": "assistant", "content": [{"type": "text", "text": "It is red."}]}, {"role": "user", "content": [{"type": "text", "text": "Thanks"}]}])),
    ("reasoning_content", raw_vars([{"role": "user", "content": "What is 2+2?"}, {"role": "assistant", "content": "The answer is 4.", "reasoning_content": "Simple arithmetic."}, {"role": "user", "content": "And 3+3?"}, {"role": "assistant", "content": "6", "reasoning_content": "Also simple."}], agp=False)),
    ("reasoning_tool", raw_vars([ASK, {"role": "assistant", "content": "", "reasoning_content": "I should call the tool.", "tool_calls": [raw_call(WEATHER_ARGS)]}, {"role": "tool", "tool_call_id": "call00001", "content": "22"}], tools=TOOLS)),
    ("documents", raw_vars([SYS, {"role": "user", "content": "Which city is the capital of France?"}], documents=DOCS, citation_mode="accurate")),
    ("kwargs_off", raw_vars([SYS, {"role": "user", "content": "Hello"}], agp=True, enable_thinking=False, thinking=False, reasoning_effort="low", date_string="1 Jan 2025")),
    ("none_tools_nogen", raw_vars([{"role": "user", "content": "Hello"}, {"role": "assistant", "content": "Hi!"}], agp=False)),
]


def build_raw(corpus):
    cases = []
    for entry in corpus:
        files = [entry["template"]] + [entry[k] for k in ("tool_use", "rag") if entry.get(k)]
        for f in files:
            stem = f[: -len(".jinja")].replace(".", "_")
            for name, _ in RAW:
                cases.append({"name": f"{stem}__{name}", "template": f, "fixture": name})
    return cases


LEX = [
    "plain text",
    "a {{ b }} c",
    "{{ 1 + 2.5 * 3 // 4 ** -1 }}",
    "{{ 'it\\'s' ~ \"q\\\"\" ~ 'tab\\tnl\\n' }}",
    "{{ 1_000 + 0x1F + 0o17 + 0b101 + 1e3 + 2.5E-2 }}",
    "{% if a == b and c != d or not e %}x{% endif %}",
    "{%- for x in y -%}\n  {{ x }}\n{%- endfor %}",
    "  {% if true %}\n  kept\n  {% endif %}\n",
    "{# comment #}a{#- trimmed -#} b",
    "{% raw %}{{ not parsed }}{% endraw %}",
    "{{ a[1:2] }}{{ b.c(d, e=f, *g, **h) }}",
    "{{ x|default('y')|upper }}{{ y is not none }}",
    "line1\nline2\n{{ x }}\n",
    "{% set ns = namespace(a=1) %}{% set ns.a = ns.a + 1 %}",
    "{{ (1, 2) }}{{ [1, [2]] }}{{ {'k': {'n': 1}} }}",
    "{%+ if x %}y{% endif +%}\n",
    "{{- x -}}  \n  {{ y }}",
    "{% macro m(a, b=2) %}{{ a }}{% endmacro %}",
]
# Every operator, whitespace control marker, raw and comment form, string escape and number literal, lexed only
LEX_ONLY = [
    "{{ a + b - c * d / e // f % g ** h ~ i }}",
    "{{ a == b != c < d <= e > f >= g }}{{ a|b }}{{ (a, [b], {c: d}) }}{{ a.b; c }}{{ x = 1 }}",
    "{%- if a -%} b {%- endif -%}",
    "  {{- a -}}  ",
    "x  {%+ if a +%}  y  {% endif %}",
    "{% raw -%}  {{ x }}  {%- endraw %}",
    "a {%- raw %} b {% endraw -%} c",
    "{#- x -#} a {#+ y +#} b",
    "  {# own line #}\n  text",
    "{{ '\\n\\t\\r\\\\\\'\\\"' }}{{ \"\\x41\\u00e9\\U0001F600\\N{BULLET}\\101\" }}",
    "{{ 'a\\\nb' }}{{ 'multi\nline' }}",
    "{{ 0 }}{{ 007 }}{{ 0_0 }}{{ 1_2_3 }}{{ 0B1_0 }}{{ 0O7_7 }}{{ 0Xf_F }}",
    "{{ 1.5 }}{{ 1e10 }}{{ 1E-5 }}{{ 1_0.2_5 }}{{ 1.5e+3 }}{{ 3.0e0 }}{{ 1. }}",
    "{{ 1e999 }}{{ 0.1 }}{{ 123456789012345678901234567890 }}",
    "{{ héllo }}{{ _x1 }}{{ 中文 }}",
    "a\r\nb\rc\n{{ x }}\r\n",
    "{% for x in y %}\n{{ x }}\n{% endfor %}\n",
    "    {%- if a %}\n    b\n    {%- endif %}\n",
    "{{ a }}{% if b %}{# c #}{% endif %}",
    "{{ true }}{{ none }}{{ True }}",
    "",
]

PARSE = LEX + [
    "{{ a if b else c }}{{ a if b }}",
    "{{ -a ** 2 }}{{ not a in b }}{{ a not in b }}",
    "{{ a < b <= c > d >= e == f != g }}",
    "{{ a ~ b ~ c }}{{ a + b - c }}{{ a * b / c % d // e }}",
    "{{ x is divisibleby 3 }}{{ x is in [1] }}{{ x is defined and y is undefined }}",
    "{{ f(1)(2)[3].four }}",
    "{{ a[::2] }}{{ a[1:] }}{{ a[:-1] }}{{ a[::] }}",
    "{{ x|join(', ', attribute='a') }}{{ x|map('upper')|list }}",
    "{% for k, v in d.items() if k recursive %}{{ loop(v) }}{% else %}none{% endfor %}",
    "{% if a %}1{% elif b %}2{% elif c %}3{% else %}4{% endif %}",
    "{% set a, b = 1, 2 %}{% set c %}block{% endset %}{% set d | upper %}x{% endset %}",
    "{% macro m(a, b=1, c='x') %}{{ caller() }}{{ varargs }}{{ kwargs }}{% endmacro %}{% call(x) m(1) %}{{ x }}{% endcall %}",
    "{% filter upper|trim %} hi {% endfilter %}",
    "{% with a = 1, b = 2 %}{{ a + b }}{% endwith %}",
    "{% for x in y %}{% if x %}{% break %}{% else %}{% continue %}{% endif %}{% endfor %}",
    "{% autoescape true %}{{ '<b>' }}{% endautoescape %}",
    "{{ {'a': 1, 'b': [2, 3]}['b'][0] }}{{ (1,) }}{{ () }}",
    "{{ \"%s-%s\"|format(1, 2) }}{{ '{}'.format(x) }}",
    "{{ x|tojson(indent=2) }}{{ messages[0]['content'] }}",
    "{% set ns.a, ns.b = 1, 2 %}",
    "{{ true }}{{ True }}{{ none }}{{ None }}{{ false }}",
    "{{ a.b.c[d][e] }}",
    "{{ x | selectattr('role', 'equalto', 'user') | list | length }}",
    "{{ 'a' 'b' }}",
]

# Every statement kind and operator precedence level the engine parses
PARSE += [
    "{{ a or b and not c }}{{ not a or b }}{{ a and b or c and d }}",
    "{{ a + b * c ** d ** e }}{{ -a ** b }}{{ (-a) ** b }}{{ a - -b }}{{ +a }}",
    "{{ a ~ b + c }}{{ a + b ~ c }}{{ a < b + c }}{{ a ~ b < c }}",
    "{{ a in b }}{{ a not in b }}{{ not a in b }}{{ a in b in c }}",
    "{{ a is b }}{{ a is not b }}{{ a is b c }}{{ a is b(c, d=e) }}{{ not a is b }}{{ a is b and c }}",
    "{{ a|b|c(d)|e(f, g=h) }}{{ -a|b }}{{ (a + b)|c }}{{ a.b|c }}",
    "{{ a if b else c if d else e }}{{ (a if b else c) ~ d }}{{ a or b if c else d }}",
    "{{ a[b][c] }}{{ a[b:c:d] }}{{ a[:] }}{{ a[::-1] }}{{ a[b, c] }}{{ a[1:2, 3] }}",
    "{{ a.b() }}{{ a.0 }}{{ a(*b) }}{{ a(**b) }}{{ a(b, *c, d=e, **f) }}{{ a(b=1, c=2) }}",
    "{{ () }}{{ (a,) }}{{ (a, b) }}{{ [] }}{{ [a,] }}{{ {} }}{{ {a: b,} }}{{ a, b }}",
    "{% set a = b %}{% set a, b = c %}{% set (a, b) = c %}{% set a.b = c %}{% set a = b, c %}",
    "{% set a %}x{% endset %}{% set a | upper | trim %}x{% endset %}{% set ns.a %}x{% endset %}",
    "{% for a in b %}{% endfor %}{% for a, b in c %}{% endfor %}{% for (a, b) in c if d %}{% else %}e{% endfor %}",
    "{% for a in b recursive %}{{ loop(a) }}{% endfor %}{% for a in b if c recursive %}{% endfor %}",
    "{% if a %}{% elif b %}{% else %}{% endif %}{% if a %}b{% endif %}",
    "{% macro m() %}{% endmacro %}{% macro m(a, b=1, c=x|y) %}{{ varargs }}{{ kwargs }}{{ caller }}{% endmacro %}",
    "{% call m() %}x{% endcall %}{% call(a, b=2) m(1) %}{{ a }}{% endcall %}",
    "{% filter upper %}a{% endfilter %}{% filter indent(2, true) %}a{% endfilter %}",
    "{% with %}{% endwith %}{% with a = 1 %}{{ a }}{% endwith %}{% with a = 1, b = a %}{% endwith %}",
    "{% autoescape false %}x{% endautoescape %}{% autoescape a %}x{% endautoescape %}",
    "{% for a in b %}{% break %}{% continue %}{% endfor %}",
    "{% print a %}{% print a, b %}",
    "{{ a.b.c(d)[e].f }}{{ a(b)(c) }}{{ 'a'.upper() }}{{ [1][0] }}{{ {'a': 1}.a }}",
    "{{ 1 }}{{ 1.5 }}{{ 'a' 'b' 'c' }}{{ 'a' ~ 1 }}{{ -1 }}{{ - 1.5 }}{{ not none }}",
    "{{ raise_exception('x') }}{{ range(3)|list }}{{ namespace(a=1).a }}{{ dict(a=1) }}",
    "{{ a // b % c / d * e }}{{ a ** -b }}{{ a == b == c }}{{ a < b > c }}",
]

# Malformed or refused sources, each compiled without rendering, lexer errors raised where jinja2's lazy lexer reaches them
SYNTAX_ERRORS = [
    "{{ 'open }}", "{{ a ) }}", "{{ (a] }}", "{{ a $ b }}", "{{ a ) }}{{ 'open }}", "{% if %}{{ a ) }}",
    "{{ x", "{{ x }", "{% if x %}", "{% if x", "{# open", "{% raw %}open", "{{ 1 2 }}", "{{ 1.5 2 }}", "{{ 'a' 'b' 3 }}",
    "{{ a b }}", "{{ a, }}x{{ }}", "{{ }}", "{% %}", "{% foo %}", "{% endfor %}", "{% for x in y %}{% endif %}",
    "{% for x in %}{% endfor %}", "{% for in y %}{% endfor %}", "{% for x y %}{% endfor %}", "{% for x in y recursive if z %}{% endfor %}",
    "{% if x %}{% else %}{% else %}{% endif %}", "{% if x %}{% elif %}{% endif %}", "{% set %}", "{% set x %}",
    "{% set x = %}", "{% set 1 = 2 %}", "{% set x.y.z = 1 %}", "{% set x() = 1 %}", "{% macro %}{% endmacro %}",
    "{% macro m(a=1, b) %}{% endmacro %}", "{% macro m(a.b) %}{% endmacro %}", "{% macro m(a %}{% endmacro %}",
    "{% call %}{% endcall %}", "{% call m %}{% endcall %}", "{% filter %}{% endfilter %}", "{% with a %}{% endwith %}",
    "{% with a = %}{% endwith %}", "{% block %}{% endblock %}", "{% block a-b %}{% endblock %}", "{% block a %}{% endblock b %}",
    "{% block a %}{% endblock %}{% block a %}{% endblock %}", "{% autoescape %}{% endautoescape %}",
    "{{ a|1 }}", "{{ a is 1 }}", "{{ a.1b }}", "{{ a[ }}", "{{ a[] }}", "{{ a( }}", "{{ a(b=1, c) }}", "{{ a(**b, c) }}",
    "{{ a(*b, *c) }}", "{{ a(**b, **c) }}", "{{ {a} }}", "{{ {a: } }}", "{{ [a b] }}", "{{ (a b) }}",
    "{{ a if }}", "{{ a if b else }}", "{{ not }}", "{{ - }}", "{{ a ~ }}", "{{ a ** }}", "{{ a in }}", "{{ a not b }}",
    "{{ x|nofilter }}", "{{ x is notest }}", "{% if x %}{{ y|nofilter }}{% endif %}{{ z|nofilter2 }}",
    "{{ false and (1|nofilter) }}", "{{ true or (1|nofilter) }}", "{{ x|nofilter }}{{ 10 ** 5000 }}", "{{ 10 ** 5000 }}{{ x|nofilter }}",
    "{% for loop in x %}{% endfor %}", "{% for x in y %}{% set loop = 1 %}{% endfor %}", "{% for x, loop in y %}{% endfor %}",
    "{% for x in y|nf_iter if z|nf_test %}{% endfor %}", "{% for x in y|nf_iter recursive %}{{ x|nf_body }}{% endfor %}",
    "{% macro m(a=x|nf_def) %}{{ y|nf_body }}{% endmacro %}", "{% call m(x|nf_call) %}{{ y|nf_body }}{% endcall %}",
    "{% block a %}{{ x|nf_block }}{% endblock %}{{ y|nf_root }}", "{% break %}{{ x|nofilter }}",
    "{{ [x|nofilter, (10**5000) ~ 'a'] }}", "{{ (x|nofilter) ~ ((10**5000) ~ 'a') }}", "{% set y = 1 if true else ((10**5000) ~ 'a') %}",
    "{{ {[1]: 2} ~ '' }}", "{% autoescape {[1]: 2} %}{% endautoescape %}", "{{ [10**5000, x|nf1] }}", "{{ f(10**5000, x|nf1) }}",
    "{{ 1" + "0" * 4300 + " }}", "{{ 0" * 1 + "0" * 4400 + " }}", "{{ 1_" + "0_" * 4400 + "0 }}", "{{ x ) }}{{ 1" + "0" * 4300 + " }}",
    "{{ 1" + "0" * 4300 + " }}{{ x ) }}", "{% if %}{{ 1" + "0" * 4300 + " }}", "{{ 0x" + "f" * 3600 + " }}",
    "{{ 1" + "0" * 4299 + " > 0 }}", "{{ 0x" + "f" * 3500 + "|string|length }}", "{% set x = 10 ** 5000 %}",
    "{% macro m(a=10**5000) %}{% endmacro %}", "{% if true %}{% else %}{{ 10 ** 5000 }}{% endif %}",
    "{{ é́x }}", "{{ a² }}", "{{ '\\x4' }}", "{{ '\\u12' }}", "{{ '\\N{NO SUCH NAME}' }}", "{{ '\\N{' }}",
    "{% if x %}a{% endif %}{% endif %}", "{% for x in y %}{% if x %}{% endfor %}{% endif %}", "{% generation %}x",
    "\n\n{{ a ) }}", "line1\n{% if x %}\n", "{% for x in y %}\n{% endif %}", "a\n{{ x is }}", "\n{% break %}", "\n\n{{ x|nofilter }}",
    "x\n{% block a %}{% endblock %}\n{% block a %}{% endblock %}", "{% macro m(caller) %}\n{{ caller }}{% endmacro %}",
    "{% macro m(a, caller) %}{{ caller() }}{% endmacro %}", "{% macro m(caller=1, b) %}{{ caller }}{% endmacro %}",
    "{% macro m(a, a) %}{% endmacro %}", "{% call(a, a) m() %}{% endcall %}", "{{ f(__debug__=1) }}", "{{ f(a=1, a=2, class=3) }}",
    "{{ f(a=1, b=2, b=3, a=4) }}", "{{ f(b=g(c=1, c=2), a=1, a=3) }}", "{% call m(caller=1) %}{% endcall %}", "{{ x|default(a=1, a=2) }}",
    "{% for x in y %}{% else %}{% break %}{% endfor %}", "{% for x in y recursive %}{% macro m() %}{% continue %}{% endmacro %}{% endfor %}",
    "{% for x in y %}{% extends 'b' %}{% endfor %}", "{% macro m() %}{% extends 'b' %}{% endmacro %}", "{% block a %}{% extends 'b' %}{% endblock %}",
    "{% if x %}{% extends 'b' %}{% endif %}{{ y }}", "{% extends 'a' %}{% extends 'b' %}", "{% for a, in b %}{% endfor %}",
    "{% from 'x' import a 'with' context %}", "{% include 'x' ignore context %}", "{# open", "{#", "text {% raw %}", "{% raw %}x",
    "{% macro m(a) %}{% set caller = 1 %}{{ caller }}{% endmacro %}", "{% set ns.a, ns.b = 1, 2 %}\n{% break %}",
    "{{ {'a': 1}|tojson }}\n{% continue %}", "{% filter upper %}\n{% break %}{% endfilter %}",
]

def lex(env, src):
    out = []
    for tok in env.lexer.tokenize(src):
        v = tok.value
        if isinstance(v, bool) or v is None:
            vs = repr(v)
        elif isinstance(v, (int, float)):
            vs = repr(v)
        else:
            vs = json.dumps(v, ensure_ascii=False)
        out.append(f"{tok.lineno} {tok.type} {vs}")
    return out

OPS = {nodes.Add: "add", nodes.Sub: "sub", nodes.Mul: "mul", nodes.Div: "div", nodes.FloorDiv: "floordiv", nodes.Mod: "mod", nodes.Pow: "pow"}

def args(n):
    parts = []
    if n.args:
        parts.append("(args " + " ".join(dump(a) for a in n.args) + ")")
    if n.kwargs:
        parts.append("(kw " + " ".join(f"{k.key}={dump(k.value)}" for k in n.kwargs) + ")")
    if n.dyn_args is not None:
        parts.append("(star " + dump(n.dyn_args) + ")")
    if n.dyn_kwargs is not None:
        parts.append("(dstar " + dump(n.dyn_kwargs) + ")")
    return "".join(" " + p for p in parts)

def body(ns):
    out = []
    for n in ns:
        if isinstance(n, nodes.Output):
            for c in n.nodes:
                out.append(f"(text {json.dumps(c.data, ensure_ascii=False)})" if isinstance(c, nodes.TemplateData) else f"(out {dump(c)})")
        else:
            out.append(dump(n))
    return "(" + " ".join(out) + ")"

def dump(n):
    if n is None:
        return "_"
    t = type(n)
    if t is nodes.Const:
        v = n.value
        return "(const " + (json.dumps(v, ensure_ascii=False) if isinstance(v, str) else repr(v)) + ")"
    if t is nodes.Name:
        return f"(name {n.name})"
    if t is nodes.NSRef:
        return f"(nsref {n.name} {n.attr})"
    if t in (nodes.Tuple, nodes.List):
        return f"({t.__name__.lower()}" + "".join(" " + dump(i) for i in n.items) + ")"
    if t is nodes.Dict:
        return "(dict" + "".join(f" ({dump(p.key)} {dump(p.value)})" for p in n.items) + ")"
    if t is nodes.Getattr:
        return f"(getattr {dump(n.node)} {n.attr})"
    if t is nodes.Getitem:
        return f"(getitem {dump(n.node)} {dump(n.arg)})"
    if t is nodes.Slice:
        return f"(slice {dump(n.start)} {dump(n.stop)} {dump(n.step)})"
    if t is nodes.Call:
        return f"(call {dump(n.node)}{args(n)})"
    if t is nodes.Filter:
        return f"(filter {n.name} {dump(n.node)}{args(n)})"
    if t is nodes.Test:
        return f"(test {n.name} {dump(n.node)}{args(n)})"
    if t is nodes.CondExpr:
        return f"(cond {dump(n.test)} {dump(n.expr1)} {dump(n.expr2)})"
    if t in OPS:
        return f"({OPS[t]} {dump(n.left)} {dump(n.right)})"
    if t in (nodes.And, nodes.Or):
        return f"({t.__name__.lower()} {dump(n.left)} {dump(n.right)})"
    if t in (nodes.Not, nodes.Neg, nodes.Pos):
        return f"({t.__name__.lower()} {dump(n.node)})"
    if t is nodes.Concat:
        return "(concat" + "".join(" " + dump(i) for i in n.nodes) + ")"
    if t is nodes.Compare:
        return f"(compare {dump(n.expr)}" + "".join(f" ({o.op} {dump(o.expr)})" for o in n.ops) + ")"
    if t is nodes.For:
        return f"(for {dump(n.target)} {dump(n.iter)} {body(n.body)} {body(n.else_)} {dump(n.test)} {n.recursive})"
    if t is nodes.If:
        return f"(if {dump(n.test)} {body(n.body)} (" + " ".join(dump(e) for e in n.elif_) + f") {body(n.else_)})"
    if t is nodes.Assign:
        return f"(set {dump(n.target)} {dump(n.node)})"
    if t is nodes.AssignBlock:
        return f"(setblock {dump(n.target)} {dump(n.filter)} {body(n.body)})"
    if t is nodes.Macro:
        return f"(macro {n.name} (" + " ".join(a.name for a in n.args) + ") (" + " ".join(dump(d) for d in n.defaults) + f") {body(n.body)})"
    if t is nodes.CallBlock:
        return f"(callblock {dump(n.call)} (" + " ".join(a.name for a in n.args) + ") (" + " ".join(dump(d) for d in n.defaults) + f") {body(n.body)})"
    if t is nodes.FilterBlock:
        return f"(filterblock {dump(n.filter)} {body(n.body)})"
    if t is nodes.With:
        return "(with (" + " ".join(dump(x) for x in n.targets) + ") (" + " ".join(dump(x) for x in n.values) + f") {body(n.body)})"
    if t is nodes.Block:
        return f"(block {n.name} {n.scoped} {body(n.body)})"
    if t is nodes.Scope and len(n.body) == 1 and type(n.body[0]) is nodes.ScopedEvalContextModifier:
        n = n.body[0]
        return f"(autoescape {dump(n.options[0].value)} {body(n.body)})"
    if t is nodes.ScopedEvalContextModifier:
        return f"(autoescape {dump(n.options[0].value)} {body(n.body)})"
    if t is nodes.Break:
        return "(break)"
    if t is nodes.Continue:
        return "(continue)"
    raise Exception(f"no dump for {t}")


UNIT_VARS = {"s": "  Hello, World!  ", "n": 7, "f": 2.5, "l": [3, 1, 2, 2], "ls": ["b", "A", "c"], "d": {"b": 2, "a": 1}, "msgs": [{"role": "system", "content": "sys"}, {"role": "user", "content": "hi"}, {"role": "assistant", "content": ""}], "none": None, "html": "<a href='x'>T & U</a>", "url": "see https://example.com/a?b=1 now"}

# Template sources per filter, each rendered over UNIT_VARS
FILTER_UNITS = {
    "abs": ["{{ -3|abs }}", "{{ -2.5|abs }}", "{{ 'x'|abs }}"],
    "attr": ["{{ msgs[0]|attr('role') }}", "{{ d|attr('missing') is defined }}"],
    "batch": ["{{ l|batch(3)|list }}", "{{ l|batch(3, 0)|list }}"],
    "capitalize": ["{{ 'hELLO wORLD'|capitalize }}"],
    "center": ["{{ 'ab'|center(6) }}|", "{{ 'abc'|center }}|"],
    "count": ["{{ l|count }}", "{{ 'abc'|count }}"],
    "d": ["{{ missing|d('x') }}", "{{ ''|d('x', true) }}"],
    "default": ["{{ missing|default('x') }}", "{{ none|default('x') }}", "{{ 0|default(5, boolean=true) }}"],
    "dictsort": ["{{ d|dictsort }}", "{{ d|dictsort(by='value', reverse=true) }}"],
    "e": ["{{ html|e }}"],
    "escape": ["{{ html|escape }}", "{{ (html|escape)|escape }}"],
    "filesizeformat": ["{{ 1234567|filesizeformat }}", "{{ 1234567|filesizeformat(true) }}", "{{ 300|filesizeformat }}"],
    "first": ["{{ l|first }}", "{{ []|first is defined }}"],
    "float": ["{{ '3.5'|float }}", "{{ 'x'|float(1.5) }}", "{{ 4|float }}"],
    "forceescape": ["{{ (html|safe)|forceescape }}"],
    "format": ["{{ '%s=%d'|format('a', 3) }}", "{{ '%(k)s'|format(k='v') }}"],
    "groupby": ["{% for g in msgs|groupby('role') %}{{ g.grouper }}:{{ g.list|length }};{% endfor %}", "{{ msgs|groupby('role')|map(attribute='grouper')|list }}"],
    "indent": ["{{ 'a\\nb\\n\\nc'|indent(2) }}", "{{ 'a\\nb'|indent(2, true, true) }}", "{{ 'a\\nb'|indent('> ') }}"],
    "int": ["{{ '42'|int }}", "{{ '0x1f'|int(base=16) }}", "{{ '3.9'|int }}", "{{ 'x'|int(-1) }}"],
    "items": ["{{ d|items|list }}", "{{ missing|items|list }}"],
    "join": ["{{ ls|join(', ') }}", "{{ msgs|join('/', attribute='role') }}"],
    "last": ["{{ l|last }}", "{{ 'abc'|last }}"],
    "length": ["{{ l|length }}", "{{ d|length }}", "{{ 'héllo'|length }}"],
    "list": ["{{ 'abc'|list }}", "{{ d|list }}"],
    "lower": ["{{ 'HeLLo'|lower }}"],
    "map": ["{{ ls|map('upper')|list }}", "{{ msgs|map(attribute='role')|list }}", "{{ msgs|map(attribute='name', default='-')|list }}"],
    "max": ["{{ l|max }}", "{{ ls|max }}", "{{ ls|max(case_sensitive=true) }}"],
    "min": ["{{ l|min }}", "{{ msgs|min(attribute='role') }}"],
    "pprint": ["{{ d|pprint }}", "{{ html|pprint }}"],
    "reject": ["{{ l|reject('odd')|list }}", "{{ [0, 1, '', 'a']|reject|list }}"],
    "rejectattr": ["{{ msgs|rejectattr('content')|map(attribute='role')|list }}", "{{ msgs|rejectattr('role', 'equalto', 'user')|list|length }}"],
    "replace": ["{{ s|replace('l', 'L') }}", "{{ s|replace('l', 'L', 2) }}"],
    "reverse": ["{{ 'abc'|reverse }}", "{{ l|reverse|list }}"],
    "round": ["{{ 2.675|round(2) }}", "{{ 2.5|round }}", "{{ 2.1|round(0, 'ceil') }}", "{{ -2.1|round(0, 'floor') }}"],
    "safe": ["{{ html|safe }}"],
    "select": ["{{ l|select('odd')|list }}", "{{ [0, 1, '', 'a']|select|list }}", "{{ l|select('gt', 1)|list }}"],
    "selectattr": ["{{ msgs|selectattr('role', 'equalto', 'user')|map(attribute='content')|list }}", "{{ msgs|selectattr('content')|list|length }}"],
    "slice": ["{{ l|slice(3)|list }}", "{{ l|slice(3, 'x')|list }}"],
    "sort": ["{{ l|sort }}", "{{ ls|sort(case_sensitive=true) }}", "{{ msgs|sort(attribute='role')|map(attribute='role')|list }}", "{{ l|sort(reverse=true) }}"],
    "string": ["{{ l|string }}", "{{ none|string }}"],
    "striptags": ["{{ html|striptags }}", "{{ '<p>a</p>\\n<p>b</p>'|striptags }}"],
    "sum": ["{{ l|sum }}", "{{ l|sum(start=10) }}"],
    "title": ["{{ 'hello wORLD-x'|title }}"],
    "tojson": ["{{ msgs[0]|tojson }}", "{{ d|tojson(indent=2) }}", "{{ 'é<'|tojson }}", "{{ d|tojson(sort_keys=true, separators=(',', ':')) }}"],
    "trim": ["{{ s|trim }}|", "{{ 'xxaxx'|trim('x') }}"],
    "truncate": ["{{ 'hello world foo bar'|truncate(9) }}", "{{ 'hello world foo bar'|truncate(9, true) }}", "{{ 'hello world'|truncate(20) }}"],
    "unique": ["{{ ls|map('lower')|unique|list }}", "{{ l|unique|list }}"],
    "upper": ["{{ 'abc'|upper }}"],
    "urlencode": ["{{ 'a b&c'|urlencode }}", "{{ d|urlencode }}"],
    "urlize": ["{{ url|urlize }}", "{{ url|urlize(10) }}"],
    "wordcount": ["{{ s|wordcount }}"],
    "wordwrap": ["{{ 'the quick brown fox jumps'|wordwrap(10) }}", "{{ 'aaaaaaaaaaaa bb'|wordwrap(5, false) }}"],
    "xmlattr": ["<a{{ {'href': 'x', 'title': 'a&b', 'skip': none}|xmlattr }}>"],
}

# Template sources per test, each rendered over UNIT_VARS
TEST_UNITS = {
    "!=": ["{{ l|select('!=', 2)|list }}"], "<": ["{{ l|select('<', 3)|list }}"], "<=": ["{{ l|select('<=', 2)|list }}"],
    "==": ["{{ l|select('==', 2)|list }}"], ">": ["{{ l|reject('>', 2)|list }}"], ">=": ["{{ l|select('>=', 2)|list }}"],
    "boolean": ["{{ true is boolean }}", "{{ 1 is boolean }}"],
    "callable": ["{{ range is callable }}", "{{ 'x' is callable }}"],
    "defined": ["{{ n is defined }}", "{{ missing is defined }}"],
    "divisibleby": ["{{ 9 is divisibleby 3 }}", "{{ 9 is divisibleby(2) }}"],
    "eq": ["{{ 'a' is eq 'a' }}"], "equalto": ["{{ 2 is equalto 2.0 }}"],
    "escaped": ["{{ (html|e) is escaped }}", "{{ html is escaped }}"],
    "even": ["{{ 4 is even }}", "{{ 3 is even }}"],
    "false": ["{{ false is false }}", "{{ 0 is false }}"],
    "filter": ["{{ 'upper' is filter }}", "{{ 'nope' is filter }}"],
    "float": ["{{ 2.5 is float }}", "{{ 2 is float }}"],
    "ge": ["{{ 2 is ge 2 }}"], "greaterthan": ["{{ 3 is greaterthan 2 }}"], "gt": ["{{ 1 is gt 2 }}"],
    "in": ["{{ 'a' is in 'abc' }}", "{{ 4 is in l }}"],
    "integer": ["{{ 2 is integer }}", "{{ true is integer }}"],
    "iterable": ["{{ l is iterable }}", "{{ n is iterable }}", "{{ 'x' is iterable }}"],
    "le": ["{{ 3 is le 2 }}"], "lessthan": ["{{ 1 is lessthan 2 }}"], "lt": ["{{ 1 is lt 1 }}"],
    "lower": ["{{ 'abc' is lower }}", "{{ 'aBc' is lower }}"],
    "mapping": ["{{ d is mapping }}", "{{ l is mapping }}"],
    "ne": ["{{ 1 is ne 1 }}"],
    "none": ["{{ none is none }}", "{{ missing is none }}"],
    "number": ["{{ 2.5 is number }}", "{{ '2' is number }}"],
    "odd": ["{{ 3 is odd }}"],
    "sameas": ["{{ none is sameas none }}", "{{ l is sameas l }}"],
    "sequence": ["{{ l is sequence }}", "{{ 'x' is sequence }}", "{{ n is sequence }}"],
    "string": ["{{ 'x' is string }}", "{{ n is string }}"],
    "test": ["{{ 'odd' is test }}", "{{ 'upper' is test }}"],
    "true": ["{{ true is true }}", "{{ 1 is true }}"],
    "undefined": ["{{ missing is undefined }}", "{{ n is undefined }}"],
    "upper": ["{{ 'ABC' is upper }}", "{{ 'AbC' is upper }}"],
}


# Edge and failure cases per filter, verified the same way as the table above
for _name, _extra in {
    "abs": ["{{ true|abs }}", "{{ (-10 ** 30)|abs }}", "{{ none|abs }}", "{{ missing|abs }}"],
    "attr": ["{{ 'abc'|attr('upper') is callable }}", "{{ d|attr(1) }}", "{{ d|attr('__class__') is defined }}"],
    "batch": ["{{ []|batch(2)|list }}", "{{ 'abcde'|batch(2)|list }}", "{{ l|batch(0)|list }}", "{{ l|batch(2.5)|list }}", "{{ l|batch(-1)|list }}"],
    "capitalize": ["{{ ''|capitalize }}", "{{ 'éCOLE'|capitalize }}", "{{ 5|capitalize }}"],
    "center": ["{{ 'abc'|center(2) }}", "{{ 'ab'|center(5) }}|", "{{ 'ab'|center('x') }}", "{% set q = 5000 %}{{ 'ab'|center(10 ** q) }}", "{{ 'ab'|center(2 ** 62) }}", "{{ 'ab'|center(-(2 ** 63) - 1) }}"],
    "count": ["{{ d|count }}", "{{ 5|count }}", "{{ none|count }}"],
    "d": ["{{ none|d('x') }}", "{{ false|d('x', true) }}"],
    "default": ["{{ missing.attr|default('x') }}", "{{ ''|default('x') }}", "{{ missing|default }}|"],
    "dictsort": ["{{ {'b': 1, 'A': 2, 'a': 3}|dictsort }}", "{{ {'b': 1, 'A': 2}|dictsort(true) }}", "{{ d|dictsort(by='x') }}", "{{ l|dictsort }}"],
    "e": ["{{ 5|e }}", "{{ none|e }}", "{{ '\"&\\''|e }}"],
    "escape": ["{{ (html|safe)|escape }}", "{{ l|escape }}"],
    "filesizeformat": ["{{ 0|filesizeformat }}", "{{ 1|filesizeformat }}", "{{ 1000|filesizeformat }}", "{{ 1024|filesizeformat(true) }}", "{{ 10 ** 30|filesizeformat }}", "{{ 'x'|filesizeformat }}", "{{ -5000|filesizeformat }}"],
    "first": ["{{ 'abc'|first }}", "{{ d|first }}", "{{ 5|first }}"],
    "float": ["{{ ' 1e3 '|float }}", "{{ 'inf'|float }}", "{{ '1_0.5'|float }}", "{{ none|float }}", "{{ true|float }}", "{% set q = 4301 %}{{ ('1' * q)|float }}", "{{ (html|safe)|float }}"],
    "forceescape": ["{{ html|forceescape }}", "{{ 5|forceescape }}"],
    "format": ["{{ '%s'|format(l) }}", "{{ '%d'|format('x') }}", "{{ '%(a)s %(b)s'|format(a=1) }}", "{{ '%5.2f|%-5d|%x|%o|%e|%r'|format(3.14159, 7, 255, 8, 12345.678, 'q') }}", "{{ '%s %s'|format(1) }}", "{% set q = 5000 %}{{ '%d'|format(10 ** q) }}", "{% set q = 5000 %}{{ '%x'|format(10 ** q)|length }}", "{{ '%*d'|format(2 ** 63, 1) }}", "{{ '%.*f'|format(2 ** 31, 1.5) }}", "{{ '%99999999999999999999d'|format(1) }}"],
    "groupby": ["{{ msgs|groupby('missing')|list }}", "{{ [{'a': 1}, {'a': 2}, {'a': 1}]|groupby('a')|list }}", "{{ [{'a': 1}, {}]|groupby('a', default=0)|list }}", "{{ [1, 'a']|groupby(0)|list }}"],
    "indent": ["{{ ''|indent }}|", "{{ 'a\\n\\nb'|indent(2, blank=true) }}", "{{ 'a'|indent(1.5) }}", "{{ 5|indent }}", "{% set q = 5000 %}{{ 'ab'|indent(10 ** q) }}", "{{ 'ab'|indent(2 ** 62) }}"],
    "int": ["{{ '0b101'|int(base=0) }}", "{{ ' 12 '|int }}", "{{ '1_000'|int }}", "{{ 4.9|int }}", "{{ -4.9|int }}", "{{ 'inf'|int(7) }}", "{{ none|int }}", "{{ '12'|int(base=40) }}", "{% set q = 5000 %}{{ ('1' * q)|int }}", "{% set q = 5000 %}{{ ('1' * q)|int(5, base=36) }}", "{% set q = 5000 %}{{ ('0x' ~ '1' * q)|int(5, base=0)|string|length }}", "{% set q = 4299 %}{{ ('1_' * q ~ '1')|int(5)|string|length }}", "{% set q = 4300 %}{{ ('0' * q ~ '1')|int }}", "{% set q = 5000 %}{{ d[10 ** q]|int }}"],
    "items": ["{{ l|items|list }}", "{{ none|items|list }}"],
    "join": ["{{ [1, 2]|join }}", "{{ [html|safe, '<']|join('&') }}", "{{ l|join(attribute='x') }}", "{{ 5|join }}", "{% set q = 5000 %}{{ [10 ** q]|join }}"],
    "last": ["{{ []|last is defined }}", "{{ (html|safe)|last }}", "{{ d|last }}", "{{ 5|last }}"],
    "length": ["{{ 5|length }}", "{{ none|length }}", "{{ range(10 ** 3)|length }}"],
    "list": ["{{ none|list }}", "{{ 5|list }}", "{{ range(3)|list }}", "{{ d.items()|list }}"],
    "lower": ["{{ 'ÀÉ'|lower }}", "{{ 5|lower }}"],
    "map": ["{{ l|map('nofilter')|list }}", "{{ l|map|list }}", "{{ msgs|map(attribute='0')|list }}", "{{ l|map('string')|join('-') }}", "{{ l|map(attribute='x', foo=1)|list }}"],
    "max": ["{{ []|max is defined }}", "{{ [1, 'a']|max }}", "{{ msgs|max(attribute='role') }}"],
    "min": ["{{ []|min is defined }}", "{{ ls|min(case_sensitive=true) }}"],
    "pprint": ["{{ l|pprint }}", "{{ none|pprint }}", "{{ range(100)|list|pprint }}", "{% set q = 5000 %}{{ [10 ** q]|pprint }}"],
    "random": ["{{ [1]|random }}", "{{ 'a'|random }}", "{{ []|random is defined }}", "{{ 5|random }}"],
    "reject": ["{{ l|reject('nosuchtest')|list }}", "{{ l|reject('in', [1, 2])|list }}"],
    "rejectattr": ["{{ msgs|rejectattr('missing')|list|length }}"],
    "replace": ["{{ 'aaa'|replace('a', 'b', 0) }}", "{{ 'abc'|replace('', '-') }}", "{{ (html|safe)|replace('<', '[') }}", "{{ 5|replace('5', '6') }}", "{% set q = 5000 %}{{ 'aaa'|replace('a', 'b', 10 ** q) }}"],
    "reverse": ["{{ d|reverse|list }}", "{{ 5|reverse }}", "{{ (html|safe)|reverse }}"],
    "round": ["{{ 2.5|round(0, 'floor') }}", "{{ 1234|round(-2) }}", "{{ 1250|round(-2) }}", "{{ 'x'|round }}", "{{ 2.5|round(1, 'other') }}", "{{ 1e300|round(-400) }}"],
    "safe": ["{{ 5|safe }}", "{{ none|safe }}"],
    "select": ["{{ l|select('nosuchtest')|list }}", "{{ [none, 1]|select('none')|list }}"],
    "selectattr": ["{{ msgs|selectattr('missing')|list }}"],
    "slice": ["{{ []|slice(3)|list }}", "{{ l|slice(0)|list }}", "{{ 'abcde'|slice(2)|list }}"],
    "sort": ["{{ [3, 'a']|sort }}", "{{ d|sort }}", "{{ [[2, 1], [1, 2]]|sort(attribute='1') }}", "{{ [] |sort }}"],
    "string": ["{{ html|string }}", "{{ (html|safe)|string }}", "{% set q = 5000 %}{{ (10 ** q)|string }}", "{% set q = 4300 %}{{ (10 ** q - 1)|string|length }}{{ (-(10 ** q - 1))|string|length }}"],
    "striptags": ["{{ 'a <!-- c --> b &amp; &lt;'|striptags }}", "{{ 5|striptags }}"],
    "sum": ["{{ [[1], [2]]|sum(start=[]) }}", "{{ [0.1] * 10|sum }}", "{{ ['a']|sum }}", "{{ msgs|sum(attribute='x') }}"],
    "title": ["{{ \"they're bill's\"|title }}", "{{ 5|title }}"],
    "tojson": ["{{ none|tojson }}", "{{ [1.0, 1e100, 'x']|tojson }}", "{{ {1: 2}|tojson }}", "{{ html|tojson }}", "{% set q = 5000 %}{{ (10 ** q)|tojson }}", "{{ {'a': 1}|tojson(indent=2 ** 62) }}"],
    "trim": ["{{ 5|trim }}", "{{ '\\u3000x\\u3000'|trim }}|"],
    "truncate": ["{{ 'hello'|truncate(2) }}", "{{ 'hello world'|truncate(5, false, '!!') }}", "{{ 'hello world'|truncate(-(2 ** 63) - 1) }}"],
    "unique": ["{{ [[1], [1]]|unique|list }}", "{{ msgs|unique(attribute='role')|list|length }}"],
    "upper": ["{{ 'ß'|upper }}", "{{ 5|upper }}"],
    "urlencode": ["{{ none|urlencode }}", "{{ [('a', 1), ('b', 'c d')]|urlencode }}", "{{ 'é/?'|urlencode }}", "{% set q = 5000 %}{{ (10 ** q)|urlencode }}"],
    "urlize": ["{{ 'www.x.com a@b.co'|urlize(target='_blank', rel='x') }}", "{{ 'http://a.com'|urlize(-1) }}", "{{ 'mailto:a@b.co'|urlize }}"],
    "wordcount": ["{{ ''|wordcount }}", "{{ 5|wordcount }}"],
    "wordwrap": ["{{ 'a b'|wordwrap(0) }}", "{{ 'hello world'|wordwrap(5, wrapstring='|') }}", "{{ 'a-b-c-d'|wordwrap(3) }}"],
    "xmlattr": ["{{ {'a b': 1}|xmlattr }}", "{{ {}|xmlattr }}|", "{{ {'a': 1}|xmlattr(false) }}"],
}.items():
    FILTER_UNITS.setdefault(_name, []).extend(_extra)

for _name, _extra in {
    "boolean": ["{{ none is boolean }}"], "callable": ["{{ cycler is callable }}", "{{ none is callable }}"],
    "defined": ["{{ d.missing is defined }}"], "divisibleby": ["{{ 9 is divisibleby 0 }}", "{{ 'x' is divisibleby 2 }}", "{{ 7.5 is divisibleby 2.5 }}"],
    "eq": ["{{ 1 is eq true }}"], "equalto": ["{{ 'a' is equalto 'b' }}"], "escaped": ["{{ 5 is escaped }}"],
    "even": ["{{ 'x' is even }}", "{{ 2.0 is even }}"], "false": ["{{ none is false }}"], "filter": ["{{ [1] is filter }}", "{{ 5 is filter }}"],
    "float": ["{{ inf is float }}"], "ge": ["{{ 'a' is ge 1 }}"], "greaterthan": ["{{ 2 is greaterthan 2 }}"], "gt": ["{{ none is gt 1 }}"],
    "in": ["{{ 1 is in 5 }}", "{{ 'a' is in d }}"], "integer": ["{{ 2.0 is integer }}"], "iterable": ["{{ none is iterable }}", "{{ d.items() is iterable }}"],
    "le": ["{{ 2 is le 2 }}"], "lessthan": ["{{ 2 is lessthan 1 }}"], "lower": ["{{ 5 is lower }}", "{% set q = 5000 %}{{ (10 ** q) is lower }}"],
    "lt": ["{{ [1] is lt [2] }}"], "mapping": ["{{ none is mapping }}"], "ne": ["{{ 1 is ne 2 }}"], "none": ["{{ 0 is none }}"],
    "number": ["{{ true is number }}", "{{ inf is number }}"], "odd": ["{{ 2 is odd }}", "{{ 'x' is odd }}"],
    "sameas": ["{{ 1 is sameas 1.0 }}", "{{ true is sameas true }}"], "sequence": ["{{ d is sequence }}", "{{ none is sequence }}"],
    "string": ["{{ (html|safe) is string }}"], "test": ["{{ [1] is test }}"], "true": ["{{ 'x' is true }}"],
    "undefined": ["{{ none is undefined }}"], "upper": ["{{ '' is upper }}", "{{ 5 is upper }}"],
}.items():
    TEST_UNITS.setdefault(_name, []).extend(_extra)

# Operations at the bottom of a 196 to 198 deep macro recursion, rendered in a warmed process so builtins push their steady state frames
WARM_BOTTOMS = [
    "", "{{ n|string }}", "{{ s|upper }}", "{{ d.a }}", "{{ d['a'] }}", "{{ s.upper() }}", "{{ d|tojson }}", "{{ d|tojson(indent=2) }}",
    "{{ l|join(',') }}", "{{ range(3)|list }}", "{{ namespace(a=1).a }}", "{{ s is defined }}", "{{ nope }}", "{{ l|map('string')|list }}",
    "{{ msgs|selectattr('role')|list }}", "{{ d|dictsort }}", "{% for x in l %}{{ loop.index }}{% endfor %}",
    "{% for x in l %}{{ loop.cycle('a', 'b') }}{% endfor %}", "{% for x in l %}{{ x }}{% endfor %}", "{{ s ~ n }}", "{{ html|e }}",
]

# Groups rendered once before recording, so every builtin they use has reached its warm steady state
WARM_GROUPS = {"warm_recursion"}

# Nesting depths around each change of outcome jinja2 and CPython produce, compile time recursion, bracket, indentation and block limits
NESTING = [
    (lambda n: "{{ " + "(" * n + "x" + ")" * n + " }}", [69, 70]),
    (lambda n: "{{ " + "[" * n + "x" + "]" * n + " }}", [74, 75]),
    (lambda n: "{{ " + "{'a': " * n + "x" + "}" * n + " }}", [74, 75]),
    (lambda n: "{{ " + "f(" * n + "x" + ")" * n + " }}", [69, 70]),
    (lambda n: "{{ x" + "|trim" * n + " }}", [197, 198, 326, 327]),
    (lambda n: "{{ " + "x if y else " * n + "z }}", [197, 198, 244, 245]),
    (lambda n: "{{ " + "(x if y else " * n + "z" + ")" * n + " }}", [64, 65]),
    (lambda n: "{{ x" + " + x" * n + " }}", [197, 198, 326, 327]),
    (lambda n: "{{ " + "not " * n + "x }}", [197, 198, 326, 327]),
    (lambda n: "{{ " + "-" * n + "x }}", [197, 198, 326, 327]),
    (lambda n: "{{ x" + ".a" * n + " }}", [197, 198, 326, 327]),
    (lambda n: "{{ x" + "|replace(y, z)" * n + " }}", [197, 198, 325, 326]),
    (lambda n: "{{ 'a'" + "|upper" * n + " }}", [326, 327]),
    (lambda n: "{% if x %}" * n + "y" + "{% endif %}" * n, [98, 99, 243, 244]),
    (lambda n: "{% for x in y %}" * n + "z" + "{% endfor %}" * n, [20, 21, 98, 99, 243, 244]),
    (lambda n: "{% for x in y %}" * n + "{% block b %}z{% endblock %}" + "{% endfor %}" * n, [20, 21]),
    (lambda n: "{% for x in y %}" * n + "{% include 'a' ignore missing %}" + "{% endfor %}" * n, [18, 19, 20, 95, 96, 97, 98]),
    (lambda n: "{% for x in y %}" * n + "{% include 'a' %}" + "{% endfor %}" * n, [18, 19, 20, 21]),
    (lambda n: "{% macro m() %}" * n + "y" + "{% endmacro %}" * n, [98, 99, 244, 245]),
    (lambda n: "{% with a = 1 %}" * n + "y" + "{% endwith %}" * n, [243, 244]),
    (lambda n: "{% filter upper %}" * n + "y" + "{% endfilter %}" * n, [246, 247]),
    (lambda n: "{% set a %}" * n + "y" + "{% endset %}" * n, [245, 246]),
    (lambda n: "{% for x in y %}{% if x %}" * n + "z" + "{% endif %}{% endfor %}" * n, [20, 21, 49, 50, 121, 122]),
    (lambda n: "{% for x in y recursive %}" * n + "z" + "{% endfor %}" * n, [49, 50, 243, 244]),
]

# Behaviors beyond single filters, each rendered over UNIT_VARS and compared message for message
GROUP_UNITS = {
    "loop_iteration": [
        "{% for x in l %}{% for y in loop %}{{ y }}{% endfor %}|{% endfor %}",
        "{% for x in l %}{{ x }}:{{ loop|list|length }}:{{ loop.index }}|{% endfor %}",
        "{% for x in l %}{{ loop|list }}{% endfor %}",
        "{% for x in l %}{% if loop.first %}{% for y in loop %}{{ y[0] }}{% endfor %}{% endif %}{{ loop.length }}|{{ loop.index }}{% endfor %}",
        "{% for x in l|map('string') %}{{ loop|length }}{% for y in loop %}{{ y[0] }}{% endfor %}{{ loop|length }}{% endfor %}",
        "{% for x in l|map('string') %}{{ x }}{% for y in loop %}{{ loop.index }}{{ y[0] }}{% endfor %}{% endfor %}",
        "{% for x in l if x is odd %}{% for y in loop %}{{ y[0] }}{% endfor %}{{ loop.index }}{% endfor %}",
        "{% for x in l %}{{ loop|length }}{{ loop.revindex }}{% endfor %}",
        "{% for x in l|select %}{{ loop|length }}:{{ loop.index }}|{% endfor %}",
        "{% for x in l %}{% for y in loop if y[0] > 1 %}{{ y[0] }}{{ loop.length }}{% endfor %}{% endfor %}",
        "{% for x in l %}{% for y in loop %}{{ loop.length }}{{ loop.last }}{% endfor %}{{ loop.index }}{% endfor %}",
        "{% for x in l %}{{ 2 in loop }}{{ loop.index }}{% endfor %}",
        "{% for x in l %}{{ (loop|first)[0] }}{{ loop.index }}{% endfor %}",
        "{% for x in l|map('string') %}{{ (loop|first)[0] }}{{ loop.index }}{% endfor %}",
        "{% for x in l|map('string') %}{{ loop|map('first')|join(',') }}|{{ loop.index }}{% endfor %}",
        "{% for x in l %}{{ loop }}{% endfor %}",
        "{% for x in [nope, 1]|map('string') %}{% for y in loop %}{{ y[0] }}{% endfor %}{% endfor %}",
        "{% for x in l %}{% for y in loop %}{% for z in loop %}{{ z[0] }}{% endfor %}{% endfor %}{{ loop.index }}{% endfor %}",
    ],
    "sort_order": [
        "{{ [3, 'a', 1, 2]|sort(case_sensitive=true) }}", "{{ [3, 1, 'a', 2]|sort(case_sensitive=true) }}",
        "{{ ['b', 1, 'a', 3, 2]|sort(case_sensitive=true, reverse=true) }}", "{{ [1, 2, nope, 'a']|sort(case_sensitive=true) }}",
        "{{ [2, 1, 'a', nope]|sort(case_sensitive=true) }}", "{{ [nope, 'a', 2, 1]|sort }}", "{{ ['b', 'A', 'a', 'B', 'c', 'C']|sort }}",
        "{{ ['b', 'A', 'a', 'B', 'c', 'C']|sort(reverse=true) }}", "{{ [[2, 'a'], [1, 'b'], [1, 3]]|sort(case_sensitive=true) }}",
        "{% set v = range(70)|list + ['a'] + range(70)|list %}{{ v|sort(case_sensitive=true) }}",
        "{% set v = range(70)|reverse|list + range(70)|list + ['a'] %}{{ v|sort(case_sensitive=true) }}",
        "{% set v = range(40)|list + range(40)|reverse|list + range(40)|list + [none] + range(30)|list %}{{ v|sort(case_sensitive=true) }}",
        "{% set v = range(65)|map('string')|list + [1] %}{{ v|sort(case_sensitive=true, reverse=true) }}",
        "{% set v = range(200)|map('string')|list %}{{ (v + [1] + v)|sort(case_sensitive=true)|length }}",
        "{% set v = range(130)|list %}{{ (v|reverse|list + v + ['x'] + v)|sort(case_sensitive=true)|length }}",
        "{{ {'b': 1, 'a': 'x', 'c': 2}|dictsort(by='value') }}", "{{ [{'a': 2}, {'a': 'x'}, {'a': 1}]|sort(attribute='a') }}",
        "{{ [{'a': 2}, {'a': 'x'}, {'a': 1}]|groupby('a') }}",
    ],
    "loop_pulls": [
        "{% for x in l|map('string') %}{{ x }}{% if not loop.last %},{% endif %}{% endfor %}",
        "{% for x in [1, nope, 2]|map('string') %}{{ loop.last }}{% endfor %}",
        "{% for x in [nope, 1]|map('string') %}{{ loop.nextitem }}{% endfor %}",
        "{% for x in l|select('odd') %}{{ loop.length }}{% endfor %}",
        "{% for x in l|reject('odd') %}{{ loop.revindex }}|{{ loop.revindex0 }}{% endfor %}",
        "{% for x in msgs|selectattr('role', 'eq', 'user') %}{{ loop.index }}/{{ loop.length }}{% endfor %}",
        "{% for x in msgs|map(attribute='content') %}{{ loop.last }}{% endfor %}",
        "{% for x in l|unique %}{{ loop.nextitem }}{% endfor %}",
        "{% for x in l if x is odd %}{{ x }}{% if not loop.last %},{% endif %}{% endfor %}",
        "{% for x in l if x > 1 %}{% if loop.first %}{{ loop.length }}{% endif %}{{ x }}{% endfor %}",
        "{% for x in l if x is odd %}{{ loop.revindex }}{% endfor %}",
        "{% for m in msgs if m.role != 'system' %}{{ m.role }}{{ '|' if not loop.last }}{% endfor %}",
        "{% for x in l if nope is defined %}{{ loop.last }}{% else %}none{% endfor %}",
        "{% for x in l if x is odd %}{{ loop.nextitem }}{% endfor %}",
        "{% for x in l|map('string') %}{{ loop|length }}{% endfor %}",
        "{% for x in l if x is odd %}{{ loop }}{% endfor %}",
        "{% for x in l|map('string') if x != '2' %}{{ loop.last }}{% endfor %}",
        "{% for x in l if x %}{% for y in ls if y is lower %}{{ loop.last }}{% endfor %}{{ loop.last }}{% endfor %}",
    ],
    "int_digit_limit": [
        "{{ 10 ** 5000 }}", "{% set q = 5000 %}{{ 10 ** q }}", "{% set q = 4300 %}{{ (10 ** q)|string|length }}", "{{ (10 ** 4299)|string|length }}",
        "{% set q = 5000 %}{{ (10 ** q) > 0 }}", "{% set q = 5000 %}{{ [10 ** q] }}", "{% set q = 5000 %}{{ {10 ** q: 1} }}",
        "{% set q = 5000 %}{{ ((10 ** q),) }}", "{% set q = 5000 %}{{ range(10 ** q, 10 ** q + 1) }}", "{% set q = 5000 %}{{ dict(a=10 ** q) }}",
        "{% set q = 5000 %}{{ namespace(a=10 ** q) }}", "{% set q = 5000 %}{{ {'a': 10 ** q}.items() }}", "{% set q = 5000 %}{{ -(10 ** q) }}",
        "{% set q = 4300 %}{{ (-(10 ** q))|string|length }}", "{% set q = 5000 %}{{ (10 ** q) ~ '' }}", "{% set q = 5000 %}{{ '%s' % (10 ** q) }}",
        "{% set q = 5000 %}{{ '%r' % (10 ** q) }}", "{% set q = 5000 %}{{ '%a' % (10 ** q) }}", "{% set q = 5000 %}{{ '%i' % (10 ** q) }}",
        "{% set q = 5000 %}{{ '%c' % (10 ** q) }}", "{% set q = 5000 %}{{ '%e' % (10 ** q) }}", "{% set q = 5000 %}{{ ('%o' % (10 ** q))|length }}",
        "{% set q = 5000 %}{{ '{}'.format(10 ** q) }}", "{% set q = 5000 %}{{ '{!r}'.format(10 ** q) }}", "{% set q = 5000 %}{{ '{!a}'.format(10 ** q) }}",
        "{% set q = 5000 %}{{ '{:,}'.format(10 ** q) }}", "{% set q = 5000 %}{{ '{:_}'.format(10 ** q) }}", "{% set q = 5000 %}{{ '{:n}'.format(10 ** q) }}",
        "{% set q = 5000 %}{{ '{:d}'.format(10 ** q) }}", "{% set q = 5000 %}{{ '{:>10}'.format(10 ** q) }}", "{% set q = 5000 %}{{ '{:x}'.format(10 ** q)|length }}",
        "{% set q = 5000 %}{{ '{:b}'.format(10 ** q)|length }}", "{% set q = 5000 %}{{ '{:e}'.format(10 ** q) }}", "{% set q = 5000 %}{{ '{:c}'.format(10 ** q) }}",
        "{{ '{:c}'.format(2 ** 63) }}", "{{ '{:c}'.format(2 ** 40) }}", "{% set q = 5000 %}{{ ('%s'|safe) % (10 ** q) }}", "{% set q = 5000 %}{{ ('%r'|safe) % (10 ** q) }}",
        "{% set q = 5000 %}{{ d[10 ** q].x }}", "{% set q = 5000 %}{{ d[10 ** q] }}|", "{% set q = 5000 %}{{ d[10 ** q]|float }}",
        "{% set q = 5000 %}{{ raise_exception(10 ** q) }}", "{% set q = 5000 %}{{ {}[10 ** q:] }}", "{% set q = 5000 %}{{ [1, 2].index(10 ** q) }}",
        "{% set q = 5000 %}{{ range(3).index(10 ** q) }}", "{{ range(3).index('x') }}", "{{ (1, 2).index(3) }}", "{% set q = 5000 %}{{ 'a' in (10 ** q) }}",
        "{% set q = 5000 %}{% macro m(a) %}{{ a }}{% endmacro %}{{ m(10 ** q) }}", "{% set q = 5000 %}{% set x %}{{ 10 ** q }}{% endset %}",
        "{% set q = 5000 %}{% filter upper %}{{ 10 ** q }}{% endfilter %}", "{% set q = 5000 %}{{ (10 ** q)|e }}", "{% set q = 5000 %}{{ (10 ** q)|safe }}",
        "{% set q = 5000 %}{% autoescape true %}{{ 10 ** q }}{% endautoescape %}", "{% set q = 5000 %}{{ {'a': 10 ** q}|xmlattr }}",
        "{% set q = 5000 %}{{ (10 ** q).bit_length() }}", "{% set q = 5000 %}{{ [10 ** q]|map('string')|list }}", "{% set q = 5000 %}{{ msgs|map(attribute='1' * q)|list }}",
        "{% set q = 5000 %}{{ ('1' * q)|int(base=8) > 0 }}", "{% set q = 4301 %}{{ ('1' * q ~ 'x')|int(5) }}", "{% set q = 4301 %}{{ ('-' ~ '1' * q)|int(5) }}",
        "{% set q = 4301 %}{{ ('0.' ~ '1' * q)|int(5) }}", "{% set q = 300 %}{{ ('1' * q ~ '.5')|int(5)|string|length }}", "{{ 'x'|int }}{{ ('1' * 300 ~ 'x')|int(9) }}",
        "{% if false %}{{ 10 ** 5000 }}{% endif %}", "{% if false %}{{ (10 ** 5000) is defined }}{% endif %}", "{% if false %}{{ 10 ** 5000 if false else 1 }}{% endif %}",
        "{{ [10 ** 5000, 1]|length }}", "{{ [10 ** 5000, inf]|length }}", "{{ (10 ** 2200) * (10 ** 2200) > 0 }}", "{{ 1 if true else ((10 ** 5000) ~ 'a') }}",
        "{{ x ~ (1 if true else ((10 ** 5000) ~ 'a')) }}", "{{ false and ((10 ** 5000) ~ 'a') }}", "{% autoescape x %}{{ 10 ** 5000 }}{% endautoescape %}",
    ],
    "compile_time": [
        "{% autoescape x %}{{ '<b>' }}{% endautoescape %}", "{% autoescape n %}{{ '<b>' ~ 'c' }}{{ '<b>'|upper }}{% endautoescape %}",
        "{% autoescape true %}{% autoescape none %}{{ '<b>' }}{% endautoescape %}{% endautoescape %}", "{% autoescape true %}{% block b %}{{ html }}{% endblock %}{% endautoescape %}",
        "{% macro m() %}{{ html }}{% endmacro %}{% autoescape true %}{{ m() }}{% endautoescape %}",
        "{% autoescape true %}{% macro m() %}{{ html }}{% endmacro %}{{ m() }}{% autoescape false %}{{ m() }}{% endautoescape %}{% endautoescape %}",
        "{% autoescape n %}{% macro m() %}{{ html }}{% endmacro %}{{ m() }}{% autoescape false %}{{ m() }}{% endautoescape %}{% endautoescape %}",
        "{% macro m(a=html ~ '') %}{{ a }}{% endmacro %}{% autoescape true %}{{ m() }}{% endautoescape %}",
        "{% if n %}{{ s|nofilter }}{% endif %}", "{% if none %}{{ s|nofilter }}{% endif %}ok", "{{ s|nofilter if none else 1 }}", "{{ s is notest if n }}",
        "{{ l|map('nofilter')|list }}", "{{ 1e999 }}", "{% set x = 1e999 %}{{ x }}", "{{ [1e999] }}", "{% set x = [1e999] %}{{ x }}",
        "{{ {}.missing }}|", "{{ {[1]: 2} }}", "{% set y = {[1]: 2} %}", "{{ [1] in {} }}", "{{ [[1]]|unique|list }}", "{{ [1] is filter }}",
    ],
    "recursion": [
        "{% macro m(n) %}{% if n > 0 %}{{ m(n - 1) }}{% endif %}{% endmacro %}{{ m(197) }}|",
        "{% macro m(n) %}{% if n > 0 %}{{ m(n - 1) }}{% endif %}{% endmacro %}{{ m(198) }}|",
        "{% macro m(n) %}{% if n > 0 %}{{ m(n - 1) }}{% else %}{{ n|string }}{% endif %}{% endmacro %}{{ m(197) }}",
        "{% block b %}{% macro m(n) %}{% if n > 0 %}{{ m(n - 1) }}{% endif %}{% endmacro %}{{ m(197) }}{% endblock %}|",
        "{% block b %}{% macro m(n) %}{% if n > 0 %}{{ m(n - 1) }}{% endif %}{% endmacro %}{{ m(198) }}{% endblock %}|",
        "{% macro m(n) %}{% if n > 0 %}{% call m(n - 1) %}x{% endcall %}{% else %}{{ caller() }}{% endif %}{% endmacro %}{% call m(97) %}y{% endcall %}",
        "{% macro m(n) %}{% if n > 0 %}{% call m(n - 1) %}x{% endcall %}{% else %}{{ caller() }}{% endif %}{% endmacro %}{% call m(98) %}y{% endcall %}",
        "{% macro m() %}{{ m() }}{% endmacro %}{{ m() }}", "{% macro m(n) %}{{ m(n - 1) if n > 0 else m(1, 2) }}{% endmacro %}{{ m(195) }}",
        "{% set ns = namespace(d=[]) %}{% for i in range(246) %}{% set ns.d = [ns.d] %}{% endfor %}{% for x in [ns.d] recursive %}{{ loop(x) }}{% endfor %}|",
        "{% set ns = namespace(d=[]) %}{% for i in range(247) %}{% set ns.d = [ns.d] %}{% endfor %}{% for x in [ns.d] recursive %}{{ loop(x) }}{% endfor %}|",
    ],
    "nesting": [build(n) for build, ns in NESTING for n in ns],
    "warm_recursion": [
        "{% macro m(n) %}{% if n > 0 %}{{ m(n - 1) }}{% else %}" + bottom + "{% endif %}{% endmacro %}{{ m(" + str(n) + ") }}"
        for bottom in WARM_BOTTOMS for n in (196, 197, 198)
    ],
    "includes": [
        "{% include self %}", "{% include none %}", "{% include [] %}", "{% include [] ignore missing %}ok", "{% include none ignore missing %}ok",
        "{% include 'x' %}", "{% include ['x', 'y'] %}", "{% include nope %}", "{% include [nope] %}", "{% include 5 %}", "{% include 5 ignore missing %}",
        "{% include range(3) %}", "{% include {} %}", "{% include d %}", "{% include l|select('none') %}", "{% include l|select %}",
        "{% include 'x' ignore missing with context %}", "{% include (1, 2) %}", "{% include true %}", "{% include 0 %}", "{% include '' %}",
        "{% include html|safe %}", "{% include s %}", "{% include l %}", "{% import self as x %}", "{% from self import a %}", "{% extends self %}",
        "{% import none as x %}", "{% from [] import a %}", "{% extends [] %}", "{% include 'x' %}{% include 'x' %}",
    ],
    "callers": [
        "{% macro m(caller=none, b=1) %}{{ caller }}{% endmacro %}{{ m(1) }}", "{% macro m(caller=none, b=1) %}{{ caller }}{% endmacro %}{{ m(1, 2) }}",
        "{% macro m(a, caller=none) %}{{ caller }}{% endmacro %}{{ m(1) }}|", "{% macro m() %}{{ caller() }}{% endmacro %}{{ m(caller=none) }}",
        "{% macro m(caller=none, b=1) %}{{ caller }}{{ kwargs }}{% endmacro %}{{ m(1) }}", "{% macro m(caller=1) %}{{ caller }}{% endmacro %}{{ m.caller }}",
        "{% macro m(caller=none, b=1) %}{{ caller }}{% endmacro %}{{ m(1, caller=2) }}", "{% macro m(caller=none, b=1, c=2) %}{{ caller }}{{ varargs }}{% endmacro %}{{ m(1) }}",
        "{% macro m(b, caller=none) %}{{ caller }}{% endmacro %}{% call m(1) %}x{% endcall %}", "{% macro m() %}{% for caller in [1] %}{% endfor %}{{ caller }}{% endmacro %}{{ m() }}|",
        "{% macro m() %}{% set caller = 2 %}{{ caller }}{% endmacro %}{% call m() %}x{% endcall %}", "{% macro m() %}{{ caller }}{% endmacro %}{{ m.caller }}",
    ],
    "memory": [
        "{% set q = 5000 %}{{ 'ab'|center(10 ** q) }}", "{% set q = 5000 %}{{ 'ab'.center(10 ** q) }}", "{% set q = 5000 %}{{ 'ab'.ljust(10 ** q) }}",
        "{% set q = 5000 %}{{ 'ab'.rjust(10 ** q) }}", "{% set q = 5000 %}{{ 'ab'.zfill(10 ** q) }}", "{% set q = 5000 %}{{ 'a\\tb'.expandtabs(10 ** q) }}",
        "{% set q = 5000 %}{{ 'a b c'.split(' ', 10 ** q) }}", "{% set q = 5000 %}{{ 'ab' * 10 ** q }}", "{% set q = 5000 %}{{ [1] * 10 ** q }}",
        "{{ 'ab'.center(2 ** 63) }}", "{{ 'ab'.center(2 ** 62) }}", "{{ 'ab'.zfill(2 ** 62) }}", "{{ 'ab' * 2 ** 62 }}", "{{ [1] * 2 ** 62 }}",
        "{{ '%*d' % (2 ** 62, 1) }}", "{{ '%*d' % (2 ** 63, 1) }}", "{{ '%.*f' % (2 ** 31, 1.5) }}", "{{ '%.2147483647f' % 1.5 }}", "{{ '{:99999999999999999999}'.format(1) }}",
        "{{ 'a b c'.split(' ', 2 ** 62) }}", "{{ 'aaa'.replace('a', 'b', 2 ** 62) }}", "{{ [1, 2, 3]|batch(2 ** 63)|list }}", "{{ 'abc'[2 ** 63] }}|",
        "{{ 'abc'[-(2 ** 63) - 1:] }}", "{{ [1, 2].index(1, -(2 ** 63) - 1) }}", "{{ 'abc'.find('b', 2 ** 63) }}", "{{ 2.5|round(2 ** 63) }}",
        "{{ 'hello world'|wordwrap(-(2 ** 63) - 1) }}", "{{ 'x http://a.com'|urlize(-(2 ** 63) - 1) }}", "{{ 'hello world'|truncate(5, leeway=-(2 ** 63) - 1) }}",
        "{{ 10 ** -5 }}{{ 2 ** 100 }}{{ (-2) ** 3 }}{{ 0 ** 0 }}",
    ],
}


# Random templates whose deepest Python frames during jinja2's parse and code generation phases are recorded
PEAK_NAMES = ["x", "y", "z", "loop", "ns", "messages", "a_b"]
PEAK_FILTERS = ["upper", "trim", "default", "join", "replace", "length", "string", "int", "tojson", "map"]
PEAK_TESTS = ["defined", "none", "string", "eq", "in", "divisibleby", "odd"]
def peak_nl(r):
    return "\n" if r.random() < 0.15 else " "
def peak_const(r):
    c = r.randrange(9)
    return ["1", "2.5", "'s'", '"t"', "true", "none", "1_000", "0x1f", "1e3"][c]
def peak_expr(r, d):
    if d <= 0:
        return r.choice([r.choice(PEAK_NAMES), peak_const(r)])
    k = r.randrange(24)
    e = lambda: peak_expr(r, d - 1)
    if k == 0: return "(" + e() + ")"
    if k == 1: return "[" + ", ".join(e() for _ in range(r.randrange(3))) + "]"
    if k == 2: return "{" + ", ".join(e() + ": " + e() for _ in range(r.randrange(3))) + "}"
    if k == 3: return "(" + e() + ", " + e() + ")"
    if k == 4: return e() + peak_nl(r) + r.choice(["+", "-", "*", "/", "//", "%", "**"]) + " " + e()
    if k == 5: return e() + " " + r.choice(["==", "!=", "<", ">=", "in", "not in"]) + " " + e()
    if k == 6: return e() + " and " + e()
    if k == 7: return e() + " or " + e()
    if k == 8: return "not " + e()
    if k == 9: return "-" + e()
    if k == 10: return e() + " ~ " + e()
    if k == 11: return e() + " if " + e() + (" else " + e() if r.random() < 0.6 else "")
    if k == 12: return e() + "|" + r.choice(PEAK_FILTERS)
    if k == 13: return e() + "|" + r.choice(PEAK_FILTERS) + "(" + e() + ")"
    if k == 14: return e() + " is " + r.choice(PEAK_TESTS)
    if k == 15: return e() + " is not " + r.choice(PEAK_TESTS) + " " + peak_expr(r, 0)
    if k == 16: return r.choice(PEAK_NAMES) + "." + r.choice(PEAK_NAMES)
    if k == 17: return e() + "[" + e() + "]"
    if k == 18: return r.choice(PEAK_NAMES) + "[" + r.choice(["1:", ":2", "::3", "1:2:3", ":"]) + "]"
    if k == 19: return r.choice(PEAK_NAMES) + "(" + ", ".join(e() for _ in range(r.randrange(3))) + ")"
    if k == 20: return r.choice(PEAK_NAMES) + "(a=" + e() + ", *" + e() + ", **" + e() + ")"
    if k == 21: return e() + " is " + r.choice(PEAK_TESTS) + "(" + e() + ")"
    if k == 22: return r.choice(PEAK_NAMES) + ".0"
    return "'a' 'b'"
def peak_stmts(r, d, inloop=False):
    out = []
    for _ in range(r.randrange(1, 4)):
        out.append(peak_stmt(r, d, inloop))
    return "".join(out)
def peak_stmt(r, d, inloop):
    k = r.randrange(22 if d > 0 else 6)
    e = lambda: peak_expr(r, r.randrange(4))
    ws = r.choice(["", "-", "+"])
    if k == 0: return "text" + peak_nl(r)
    if k == 1: return "{{" + ws + " " + e() + " }}"
    if k == 2: return "{# c #}"
    if k == 3: return "{% set " + r.choice(PEAK_NAMES) + " = " + e() + " %}"
    if k == 4: return "{% print " + e() + ", " + e() + " %}"
    if k == 5: return ("{% break %}" if inloop else "{%- raw %}{{ r }}{% endraw %}")
    sub = lambda il=inloop: peak_stmts(r, d - 1, il)
    if k == 6: return "{% if " + e() + " %}" + sub() + ("{% elif " + e() + " %}" + sub() if r.random() < 0.4 else "") + ("{% else %}" + sub() if r.random() < 0.4 else "") + "{% endif %}"
    if k == 7: return "{% for " + r.choice(["i", "i, j"]) + " in " + e() + (" if " + e() if r.random() < 0.3 else "") + (" recursive" if r.random() < 0.2 else "") + " %}" + sub(True) + ("{% else %}" + sub() if r.random() < 0.3 else "") + "{% endfor %}"
    if k == 8: return "{% macro m" + str(r.randrange(3)) + "(a, b=" + e() + ") %}" + sub(False) + "{% endmacro %}"
    if k == 9: return "{% call(q) m0(" + e() + ") %}" + sub(False) + "{% endcall %}"
    if k == 10: return "{% filter upper %}" + sub() + "{% endfilter %}"
    if k == 11: return "{% with a = " + e() + ", b = " + e() + " %}" + sub() + "{% endwith %}"
    if k == 12: return "{% autoescape " + e() + " %}" + sub() + "{% endautoescape %}"
    if k == 13: return "{% block b" + str(r.randrange(1000)) + " %}" + sub() + "{% endblock %}"
    if k == 14: return "{% set " + r.choice(PEAK_NAMES) + " | upper %}" + sub() + "{% endset %}"
    if k == 15: return "{% include " + e() + r.choice(["", " ignore missing", " without context"]) + " %}"
    if k == 16: return "{% from 'x' import a, b as c with context %}"
    if k == 17: return "{% import " + e() + " as q %}"
    if k == 18: return "{% generation %}" + sub() + "{% endgeneration %}"
    if k == 19: return "{% set ns.a = " + e() + " %}"
    if k == 20: return "{% set (a, b) = " + e() + " %}"
    return "{{ " + "(" * r.randrange(1, 30) + e() + ")" * r.randrange(1, 30) + " }}"
def peak_templates(seed, n):
    r = random.Random(seed)
    out = []
    for i in range(n):
        s = peak_stmts(r, r.randrange(1, 5))
        if r.random() < 0.15:
            cut = r.randrange(len(s) + 1)
            s = s[:cut]
        elif r.random() < 0.15:
            cut = r.randrange(len(s) + 1)
            s = s[:cut] + r.choice(["$", ")", "]", "}", "'", "{#", "{% raw %}", "\n", "é", "1.5e", "{%", "{{", "%}", "}}", "\\x"]) + s[cut:]
        out.append(s)
    return out


PEAK_PHASES = {"_parse": "parse", "_generate": "generate"}


def peak_record(src):
    # The deepest frame chain under Environment._parse and _generate while compile_template compiles src the second time
    peaks = {}

    def tracer(frame, event, arg):
        if event == "call":
            depth, phase, f = 0, None, frame
            while f is not None:
                depth += 1
                if f.f_code.co_filename.endswith("environment.py"):
                    phase = PEAK_PHASES.get(f.f_code.co_name, phase)
                f = f.f_back
            if phase is not None and depth > peaks.get(phase, 0):
                peaks[phase] = depth

    try:
        compile_uncached(src)
    except Exception:
        pass
    sys.settrace(tracer)
    try:
        compile_uncached(src)
        record = {"src": src, "out": ""}
    except Exception as e:
        record = {"src": src, **error_record(e)}
    finally:
        sys.settrace(None)
    record["parse"], record["generate"] = peaks.get("parse", 0), peaks.get("generate", 0)
    return record


def compile_uncached(source):
    # compile_template's frame depth without its cache, which would skip sources other tables already compiled
    return compile_fresh(source)


def compile_fresh(source):
    return chat_template_utils._cached_compile_jinja_template(source)


def peak_table():
    return [peak_record(src) for src in dict.fromkeys(peak_templates(20261103, 6000))]


# Warm steady state frame costs: the deepest Python frames each builtin and runtime operation pushes above the template frame calling it
WARM = {"active": [], "recording": False, "table": {}, "costs": None}

# Filters whose first positional argument names a filter, test or attribute the call goes through
NAMED_ARG_FILTERS = {"map", "select", "reject", "selectattr", "rejectattr"}


def _frames(frame):
    # Frames on the stack ending at frame, the measuring wrappers left out
    n = 0
    while frame is not None:
        if frame.f_code is not _WRAPPER_CODE:
            n += 1
        frame = frame.f_back
    return n


def _warm_tracer(frame, event, arg):
    if event == "call" and WARM["active"] and frame.f_code is not _WRAPPER_CODE:
        depth = _frames(frame)
        for ctx in WARM["active"]:
            ctx[1] = max(ctx[1], depth)


def _measured(key_of, f):
    # Wraps f to record, under key_of's key, the deepest frame f reaches counted from its caller
    @functools.wraps(f)
    def wrapper(*args, **kwargs):
        if not WARM["recording"]:
            return f(*args, **kwargs)
        # The measuring code runs with no measured call active so no cost counts its frames
        outer = WARM["active"]
        WARM["active"] = []
        try:
            keyed = key_of(args, kwargs)
            base = _frames(sys._getframe(1))
        finally:
            WARM["active"] = outer
        key, subject = keyed if keyed is not None else (None, None)
        ctx = [base, base]
        WARM["active"].append(ctx)
        try:
            result = f(*args, **kwargs)
        finally:
            WARM["active"].remove(ctx)
            if key is not None:
                WARM["active"] = []
                try:
                    features = shape_features(subject)
                finally:
                    WARM["active"] = outer
                WARM["table"].setdefault(key, []).append((ctx[1] - base, features))
                if WARM["costs"] is not None:
                    WARM["costs"][key] = max(WARM["costs"].get(key, 0), ctx[1] - base)
        if key is not None and isinstance(result, types.GeneratorType):
            PRODUCERS[id(result)] = (result, key)
        return result
    return wrapper


# The key of the measured filter call that created each generator, so consuming it is keyed by what it runs
PRODUCERS = {}


def _producer(value):
    entry = PRODUCERS.get(id(value))
    return " |" + entry[1] if entry is not None and entry[0] is value else ""


_WRAPPER_CODE = _measured(None, len).__code__


def _tname(v):
    # A value's type for frame cost keys, an undefined value also naming whether it carries a hint, an object or only a name
    if isinstance(v, jinja2.runtime.Undefined):
        if v._undefined_hint:
            return "Undefined:hint"
        return "Undefined:name" if v._undefined_obj is jinja2.utils.missing else "Undefined:obj"
    return type(v).__name__


def _call_key(kind, name, f):
    skip = 1 if _PassArg.from_obj(f) is not None else 0

    def key_of(args, kwargs):
        value, pos = args[skip], args[skip + 1:]
        extra = " " + pos[0] if name in NAMED_ARG_FILTERS and pos and isinstance(pos[0], str) else ""
        return f"{kind} {name} {_tname(value)} {len(pos)} {','.join(sorted(kwargs))}{extra}{_producer(value)}", value
    return key_of


class _MeasuredMap(dict):
    # An environment's filters or tests, each callable stored measured
    kind = ""

    def __setitem__(self, name, f):
        super().__setitem__(name, _measured(_call_key(self.kind, name, f), f))


class _MeasuredDefaults(dict):
    # jinja2's default filters or tests, copied into measured maps by each new environment
    def __init__(self, src, kind):
        super().__init__(src)
        self.kind = kind

    def copy(self):
        out = _MeasuredMap()
        out.kind = self.kind
        for name, f in self.items():
            out[name] = f
        return out


def _attr_outcome(env, obj, attr, value):
    # The path the sandbox takes for a found attribute, a wrapped str format, private, internal, mutating or a safe attribute
    if env.wrap_str_format(value) is not None:
        return "format"
    if attr.startswith("_"):
        return "private"
    if jinja2.sandbox.is_internal_attribute(obj, attr):
        return "internal"
    return "mutable" if jinja2.sandbox.modifies_known_mutable(obj, attr) else "attr"


def _peekless_getattr(obj, attr):
    # getattr, except that a LoopContext's attributes are found statically since its properties pull from its iterator
    if isinstance(obj, jinja2.runtime.LoopContext):
        return inspect.getattr_static(obj, attr)
    return getattr(obj, attr)


def _getattr_outcome(env, obj, attr):
    # Which lookup the sandbox's getattr succeeds with, raise when getattr fails with an error other than AttributeError
    try:
        value = _peekless_getattr(obj, attr)
    except AttributeError:
        pass
    except Exception:
        return "raise"
    else:
        return _attr_outcome(env, obj, attr, value)
    try:
        obj[attr]
        return "item"
    except Exception:
        return "missing"


def _getitem_outcome(env, obj, key):
    # Which lookup the sandbox's getitem succeeds with, item, the attribute path for a string key, or missing
    try:
        obj[key]
        return "item"
    except Exception:
        pass
    if isinstance(key, str):
        try:
            value = _peekless_getattr(obj, key)
        except Exception:
            pass
        else:
            return _attr_outcome(env, obj, key, value)
    return "missing"


def _callee_key(args, kwargs):
    callee = args[2]
    if isinstance(callee, (jinja2.runtime.Macro, jinja2.runtime.LoopContext, jinja2.runtime.BlockReference)):
        return None
    owner = getattr(callee, "__self__", None)
    subject = owner
    owner = "-" if owner is None or isinstance(owner, type(sys)) else _tname(owner)
    return f"call {getattr(callee, '__name__', _tname(callee))} {owner}", subject


def _feature(fn):
    # The frames below fn's own frame while it runs, measured alone so no measured call around it sees them
    saved = WARM["active"]
    base = _frames(sys._getframe()) + 1
    ctx = [base, base]
    WARM["active"] = [ctx]
    try:
        fn()
    except Exception:
        pass
    finally:
        WARM["active"] = saved
    return ctx[1] - base


def _elements(v):
    # The items str() reaches iterating a container, nothing for iterators that iterating would consume
    if isinstance(v, (list, tuple, dict, str, range)):
        return list(v)
    return []


# Shape features of a value, each the frames a stdlib or jinja2 routine pushes on it, candidates for a key's cost rule
SHAPE_FEATURES = {
    "repr": lambda v: _feature(lambda: repr(v)),
    "str": lambda v: _feature(lambda: str(v)),
    "items": lambda v: max([_feature(lambda e=e: str(e)) for e in _elements(v)], default=0),
    "json": lambda v: _feature(lambda: json.dumps(v, ensure_ascii=False)),
    "pprint": lambda v: _feature(lambda: pprint.pformat(v)),
}


def _reaches_loop(v, seen):
    # Whether repr of v reaches a LoopContext, whose repr reads length and so consumes the rest of its iterator
    if id(v) in seen:
        return False
    seen.add(id(v))
    if isinstance(v, jinja2.runtime.LoopContext) or isinstance(getattr(v, "__self__", None), jinja2.runtime.LoopContext):
        return True
    if isinstance(v, jinja2.utils.Namespace):
        v = object.__getattribute__(v, "_Namespace__attrs")
    if isinstance(v, dict):
        return any(_reaches_loop(x, seen) for x in v.keys()) or any(_reaches_loop(x, seen) for x in v.values())
    if isinstance(v, (list, tuple, set, frozenset)):
        return any(_reaches_loop(x, seen) for x in v)
    return False


def shape_features(v):
    # None for a value whose repr would change what the render does next
    if _reaches_loop(v, set()):
        return None
    return tuple(f(v) for f in SHAPE_FEATURES.values())


def instrument():
    # Measures every filter, test, sandbox lookup and call, escape, concat, loop context step and undefined creation from now on
    jinja2.environment.DEFAULT_FILTERS = _MeasuredDefaults(jinja2.environment.DEFAULT_FILTERS, "filter")
    jinja2.environment.DEFAULT_TESTS = _MeasuredDefaults(jinja2.environment.DEFAULT_TESTS, "test")
    sandbox = jinja2.sandbox.SandboxedEnvironment
    sandbox.getattr = _measured(lambda a, k: (f"getattr {_tname(a[1])} {_getattr_outcome(a[0], a[1], a[2])}", a[1]), sandbox.getattr)
    sandbox.getitem = _measured(lambda a, k: (f"getitem {_tname(a[1])} {_tname(a[2])} {_getitem_outcome(a[0], a[1], a[2])}", a[1]), sandbox.getitem)
    sandbox.call = _measured(_callee_key, sandbox.call)
    escape = _measured(lambda a, k: (f"escape {_tname(a[0])}", a[0]), markupsafe.escape)
    jinja2.runtime.escape = jinja2.compiler.escape = escape
    jinja2.runtime.str_join = _measured(lambda a, k: ("concat str_join", a[0]), jinja2.runtime.str_join)
    jinja2.runtime.markup_join = _measured(lambda a, k: ("concat markup_join", a[0]), jinja2.runtime.markup_join)
    loop = jinja2.runtime.LoopContext
    loop.__init__ = _measured(lambda a, k: (f"loop __init__ {_tname(a[1])}{_producer(a[1])}", None), loop.__init__)
    for method in ("__iter__", "__next__"):
        setattr(loop, method, _measured(lambda a, k, m=method: (f"loop {m} {_tname(a[0]._iterable)}{_producer(a[0]._iterable)}", None), getattr(loop, method)))
    jinja2.runtime.Undefined.__init__ = _measured(lambda a, k: ("undefined", None), jinja2.runtime.Undefined.__init__)


def measure_warm(sources):
    # Runs each source twice, recording frame costs, value shapes and each render's costs per key on the warm second run
    instrument()
    renders = []
    sys.settrace(_warm_tracer)
    try:
        for compile_only, render in sources:
            for recording in (False, True):
                WARM["recording"] = recording
                try:
                    tmpl = compile_fresh(compile_only)
                    if render is not None:
                        WARM["costs"] = {} if recording else None
                        tmpl.render(**copy.deepcopy(render))
                except Exception:
                    pass
                if recording and render is not None:
                    renders.append({"src": compile_only, "vars": render, "costs": WARM["costs"]})
                WARM["costs"] = None
                if sys.gettrace() is not _warm_tracer:
                    raise RuntimeError("tracing stopped while measuring " + repr(compile_only[:80]))
        WARM["recording"] = False
    finally:
        sys.settrace(None)
    return WARM["table"], renders


# Operations whose frames depend on their subject and arguments past any shape feature, each computed by a Go port of its Python call path
MODELED = {
    "call format", "call generate_lorem_ipsum", "call join", "escape", "filter attr", "getattr LoopContext", "filter length", "filter count", "filter first", "filter dictsort", "filter e", "filter escape",
    "filter groupby", "filter indent", "filter join", "filter last", "filter list", "filter max", "filter min", "filter replace",
    "filter sort", "filter striptags", "filter sum", "filter urlencode", "filter wordwrap", "filter xmlattr", "loop __next__", "test in",
}


def modeled_op(key):
    return next((op for op in MODELED if key == op or key.startswith(op + " ")), None)


def _fit_rule(points):
    # The smallest step then offset with every cost equal to max(floor, offset + step * feature), one floor for points off the line
    for step in range(1, 9):
        for offset in sorted({cost - step * feature for cost, feature in points}):
            floors = {cost for cost, feature in points if cost != offset + step * feature}
            floor = floors.pop() if len(floors) == 1 else offset
            if all(cost == max(floor, offset + step * feature) for cost, feature in points):
                return floor, offset, step
    return None


def fit_rules(observations):
    # A constant cost per key, or for a key whose cost varies with the value, the shape feature its cost is a fixed base above
    table, rules, unexplained = {}, {}, []
    for key, obs in observations.items():
        costs = Counter(cost for cost, _ in obs)
        if len(costs) == 1:
            table[key] = next(iter(costs))
            continue
        if modeled_op(key) is not None:
            continue
        if all(features is not None for _, features in obs):
            for i, name in enumerate(SHAPE_FEATURES):
                fit = _fit_rule([(cost, features[i]) for cost, features in obs])
                if fit is not None:
                    rules[key] = (name, *fit)
                    break
        if key not in rules:
            unexplained.append((key, sorted({(cost, features) for cost, features in obs}, key=repr)[:12]))
    if unexplained:
        for key, sample in unexplained:
            print("no shape rule fits", key, sample, file=sys.stderr)
        raise RuntimeError(f"{len(unexplained)} frame cost keys vary with no shape rule")
    return table, rules


def coarse_levels(costs):
    # Each key's broader levels, the op with its name and type, the op with its name, the op alone, valued by their most common cost
    levels = {}
    for key, cost in costs.items():
        parts = key.split(" ")
        for n in range(1, min(len(parts), 4)):
            levels.setdefault(" ".join(parts[:n]), Counter())[cost] += 1
    return {k: max(c.most_common(), key=lambda kv: (kv[1], kv[0]))[0] for k, c in levels.items() if k not in costs}


def warm_cost_records(observations):
    # Every measured key with the distinct costs it showed and the shape features of the value each cost was measured on
    return {key: [{"cost": cost, **dict(zip(SHAPE_FEATURES, features or ()))} for cost, features in sorted(set(obs), key=repr)] for key, obs in observations.items()}


def write_frame_table(observations, table, rules):
    # Writes the Go tables warmBuiltinFrames reads, constant keys, shape dependent keys with their rule, and broader levels
    path = os.path.join(HERE, "..", "warmframes.go")
    levels = coarse_levels({key: Counter(cost for cost, _ in obs).most_common(1)[0][0] for key, obs in observations.items()})
    lines = [
        "// Code generated by testdata/generate.py from a warmed CPython 3.14 process running jinja2 3.1.6. DO NOT EDIT.",
        "",
        "package jinja",
        "",
        "// The deepest Python frames each measured operation pushes above its calling frame, keyed as builtinframes.go builds them",
        "var warmFrames = map[string]int{",
    ]
    lines += [f"\t{json.dumps(k)}: {v}," for k, v in sorted(table.items())]
    lines += ["}", "", "// Keys whose cost varies with the value, each a shape feature its cost follows above a floor", "var warmShapeRules = map[string]warmShapeRule{"]
    lines += [f"\t{json.dumps(k)}: {{{json.dumps(name)}, {floor}, {offset}, {step}}}," for k, (name, floor, offset, step) in sorted(rules.items())]
    lines += ["}", "", "// Operations whose cost the engine computes by a port of their Python call path", "var warmModeled = map[string]bool{"]
    lines += [f"\t{json.dumps(op)}: true," for op in sorted(MODELED)]
    lines += ["}", "", "// The most common cost of every measured key sharing a broader key, for operations no source measured exactly", "var warmFrameLevels = map[string]int{"]
    lines += [f"\t{json.dumps(k)}: {v}," for k, v in sorted(levels.items())]
    lines += ["}", ""]
    with open(path, "w", encoding="utf-8") as f:
        f.write("\n".join(lines))
    subprocess.run(["gofmt", "-w", path], check=True)


# Value shapes the frame costs are measured over: lists, dicts and tuples nested 0 to 8 deep around each kind of leaf, mixed members and attribute paths
GRID_LEAVES = ["1", "'a'", "('a'|safe)", "nope", "namespace(a=1)", "range(2)", "none", "'" + "ab " * 40 + "'"]
GRID_FILTERS = [
    "tojson", "tojson(indent=2)", "tojson(sort_keys=true)", "pprint", "string", "list", "length", "first", "last", "join", "unique", "sort", "min",
    "max", "sum", "reverse", "safe", "e", "forceescape", "lower", "upper", "trim", "capitalize", "title", "center", "indent", "truncate",
    "wordcount", "wordwrap", "striptags", "urlize", "urlencode", "replace('a', 'b')", "count", "dictsort", "items|list", "batch(2)|list",
    "slice(2)|list", "map('string')|list", "map('lower')|list", "select|list", "reject|list", "xmlattr", "default", "abs", "int", "float",
    "round", "filesizeformat", "attr('a')", "tojson|length",
]
GRID_TESTS = ["callable", "defined", "escaped", "float", "integer", "iterable", "lower", "mapping", "none", "number", "sequence", "string", "upper",
              "boolean", "true", "false", "undefined"]
GRID_PATH_FILTERS = ["sort(attribute=P)", "unique(attribute=P)|list", "min(attribute=P)", "max(attribute=P)", "sum(attribute=P)", "join(',', attribute=P)",
                     "map(attribute=P)|list", "selectattr(P)|list", "rejectattr(P)|list", "groupby(P)|list"]


def _nest(kind, inner, depth):
    for _ in range(depth):
        inner = {"list": "[" + inner + "]", "tuple": "(" + inner + ",)", "dict": "{'k': " + inner + "}"}[kind]
    return inner


def grid_values():
    values = list(GRID_LEAVES)
    for kind in ("list", "tuple", "dict"):
        for depth in range(1, 9):
            for leaf in GRID_LEAVES:
                values.append(_nest(kind, leaf, depth))
                mixed = {"list": "[1, " + leaf + "]", "tuple": "(1, " + leaf + ")", "dict": "{'j': 1, 'k': " + leaf + "}"}[kind]
                values.append(_nest(kind, mixed, depth - 1))
    return list(dict.fromkeys(values))


def grid_path_value(leaf):
    value = "{'a': {'b': {'c': {'d': " + leaf + "}}}}"
    return "[" + value + ", " + value + "]"


def grid_templates():
    out = []
    for value in grid_values():
        head = "{% set v = " + value + " %}"
        out.append(head + "{{ v }}")
        out += [head + "{{ v|" + f + " }}" for f in GRID_FILTERS]
        out += [head + "{{ v is " + t + " }}" for t in GRID_TESTS]
    for leaf in GRID_LEAVES:
        for n in range(1, 5):
            path = ".".join("abcd"[:n])
            head = "{% set v = " + grid_path_value(leaf) + " %}"
            out += [head + "{{ v|" + f.replace("P", repr(path)) + " }}" for f in GRID_PATH_FILTERS]
    return out


def grid_peak(src):
    # The deepest frame a warmed render of src reaches, counted from its root frame
    tmpl = compile_fresh(src)
    try:
        tmpl.render()
    except Exception:
        pass
    context = tmpl.new_context({})
    base = _frames(sys._getframe()) + 1
    peak = [base]

    def tracer(frame, event, arg):
        if event == "call":
            peak[0] = max(peak[0], _frames(frame))

    sys.settrace(tracer)
    try:
        "".join(tmpl.root_render_func(context))
        out = {"out": ""}
    except Exception as e:
        out = error_record(e)
    finally:
        sys.settrace(None)
    return {"src": src, "peak": peak[0] - base, **out}


def shape_feature_records(env, exprs):
    # Each expression's value with the frames every shape feature routine pushes on it, measured warm under the frame tracer
    sys.settrace(_warm_tracer)
    try:
        records = []
        for src in exprs:
            value = env.compile_expression(src, undefined_to_none=False)()
            shape_features(value)
            records.append({"src": src, **dict(zip(SHAPE_FEATURES, shape_features(value)))})
    finally:
        sys.settrace(None)
    return records


def compile_record(src):
    try:
        compile_template(src)
    except Exception as e:
        return error_record(e)
    return {"out": ""}


def syntax_tables(env):
    return {
        "lex": [{"src": s, "tokens": lex(env, s)} for s in LEX + LEX_ONLY],
        "parse": [{"src": s, "tree": body(env.parse(s).body)} for s in PARSE],
        "errors": [{"src": s, **compile_record(s)} for s in SYNTAX_ERRORS],
    }


def unit_tables():
    def table(units):
        out = {}
        for name, sources in units.items():
            rows = []
            for src in sources:
                try:
                    if name in WARM_GROUPS:
                        try:
                            compile_template(src).render(**copy.deepcopy(UNIT_VARS))
                        except Exception:
                            pass
                    rows.append({"src": src, "out": compile_template(src).render(**copy.deepcopy(UNIT_VARS))})
                except Exception as e:
                    rows.append({"src": src, **error_record(e)})
            out[name] = rows
        return out
    return {"vars": UNIT_VARS, "filters": table(FILTER_UNITS), "tests": table(TEST_UNITS), "groups": table(GROUP_UNITS)}


def error_record(e):
    # An exception whose str() itself raises is recorded as Python's traceback printer shows it, with the line syntax errors carry
    try:
        message = str(e)
    except Exception:
        message = "<exception str() failed>"
    record = {"error": type(e).__name__, "message": message}
    if getattr(e, "lineno", None) is not None:
        record["lineno"] = e.lineno
    return record


def source_records(env, sources):
    # The module jinja2's code generator writes for each source, or the exception compiling it raises
    records = []
    for src in dict.fromkeys(sources):
        try:
            records.append({"src": src, "code": env.compile(src, raw=True)})
        except Exception as e:
            records.append({"src": src, **error_record(e)})
    return records


def template_text(name):
    with open(os.path.join(HERE, "templates", name), encoding="utf-8") as f:
        return f.read()


def render_case(case):
    source = template_text(case["template"])
    if case.get("tools") and case.get("tool_use"):
        source = template_text(case["tool_use"])
    tmpl = compile_template(source)
    conversation = vllm_conversation(copy.deepcopy(CONVERSATIONS[case["conversation"]]), detect_content_format(tmpl, source), source)
    variables = {"bos_token": "", "eos_token": "", "documents": None, **case.get("kwargs", {})}
    return tmpl.render(messages=conversation, tools=tool_dicts(TOOL_SETS.get(case.get("tools"))), add_generation_prompt=case["add_generation_prompt"], **variables)


def render_all(items, render):
    results = {}
    for item in items:
        try:
            results[item["name"]] = {"out": render(item)}
        except Exception as e:
            results[item["name"]] = error_record(e)
    return results


def write_json(path, value, sort_keys=False):
    # Writes one record per line, gzipped with a zero mtime when the path ends in .gz
    text = json.dumps(value, ensure_ascii=False, sort_keys=sort_keys, separators=(",", ":"))
    if isinstance(value, list):
        text = "[\n" + ",\n".join(json.dumps(v, ensure_ascii=False, sort_keys=sort_keys, separators=(",", ":")) for v in value) + "\n]"
    elif isinstance(value, dict):
        text = "{\n" + ",\n".join(json.dumps(k, ensure_ascii=False) + ":" + json.dumps(value[k], ensure_ascii=False, sort_keys=sort_keys, separators=(",", ":")) for k in (sorted(value) if sort_keys else value)) + "\n}"
    data = (text + "\n").encode("utf-8")
    if path.endswith(".gz"):
        data = gzip.compress(data, mtime=0)
    with open(path, "wb") as f:
        f.write(data)


def read_json(path):
    with open(path, "rb") as f:
        data = f.read()
    if path.endswith(".gz"):
        data = gzip.decompress(data)
    return json.loads(data)


def main():
    corpus = read_json(os.path.join(HERE, "corpus.json"))
    cases, raw = build_cases(corpus), build_raw(corpus)
    write_json(os.path.join(HERE, "fixtures.json"), {"conversations": CONVERSATIONS, "tools": TOOL_SETS, "raw": dict(RAW)})
    write_json(os.path.join(HERE, "cases.json"), cases)
    write_json(os.path.join(HERE, "raw.json"), raw)
    env = compile_template("").environment
    write_json(os.path.join(HERE, "syntax.json"), syntax_tables(env))
    write_json(os.path.join(HERE, "units.json"), unit_tables())
    golden = os.path.join(HERE, "golden")
    os.makedirs(golden, exist_ok=True)
    raw_vars_by_name = dict(RAW)
    snippets = read_json(os.path.join(HERE, "snippets.json"))
    exprs = read_json(os.path.join(HERE, "exprs.json.gz"))
    results = {
        "cases.json.gz": render_all(cases, render_case),
        "raw.json.gz": render_all(raw, lambda c: compile_template(template_text(c["template"])).render(**copy.deepcopy(raw_vars_by_name[c["fixture"]]))),
        "snippets.json.gz": render_all(snippets, lambda s: compile_template(s["template"]).render(**copy.deepcopy(s.get("vars", {})))),
        "exprs.json.gz": render_all(exprs["snippets"], lambda s: compile_template(s["template"]).render(**copy.deepcopy(exprs["vars"]))),
    }
    units = read_json(os.path.join(HERE, "units.json"))
    syntax = read_json(os.path.join(HERE, "syntax.json"))
    sources = [template_text(name) for name in sorted(os.listdir(os.path.join(HERE, "templates")))]
    sources += [s["template"] for s in snippets] + [s["template"] for s in exprs["snippets"]]
    sources += [row["src"] for table in ("lex", "parse", "errors") for row in syntax[table]]
    sources += [row["src"] for table in ("filters", "tests", "groups") for rows in units[table].values() for row in rows]
    results["sources.json.gz"] = source_records(env, sources)
    results["peaks.json.gz"] = peak_table()
    for name in os.listdir(golden):
        if name not in results and name not in ("framegrid.json.gz", "shapefeatures.json.gz", "warmcosts.json.gz", "warmrender.json.gz"):
            os.remove(os.path.join(golden, name))
    for name, result in results.items():
        write_json(os.path.join(golden, name), result, True)
    warm_sources = [(src, UNIT_VARS) for group in (FILTER_UNITS, TEST_UNITS) for srcs in group.values() for src in srcs]
    warm_sources += [(src, UNIT_VARS) for name, srcs in GROUP_UNITS.items() if name not in ("memory", "nesting", "recursion", "warm_recursion") for src in srcs]
    warm_sources += [("{% macro m(n) %}{% if n > 0 %}{{ m(n - 1) }}{% else %}" + bottom + "{% endif %}{% endmacro %}{{ m(2) }}", UNIT_VARS) for bottom in WARM_BOTTOMS]
    warm_sources += [(s["template"], s.get("vars", {})) for s in snippets] + [(s["template"], exprs["vars"]) for s in exprs["snippets"]]
    warm_sources += [(row["src"], None) for row in results["peaks.json.gz"]]
    grid = grid_templates()
    write_json(os.path.join(golden, "framegrid.json.gz"), [grid_peak(src) for src in grid], True)
    warm_sources += [(src, {}) for src in grid]
    observations, renders = measure_warm(warm_sources)
    write_json(os.path.join(golden, "warmcosts.json.gz"), warm_cost_records(observations), True)
    write_json(os.path.join(golden, "warmrender.json.gz"), renders, True)
    shape_exprs = grid_values() + [grid_path_value(leaf) for leaf in GRID_LEAVES]
    write_json(os.path.join(golden, "shapefeatures.json.gz"), shape_feature_records(env, shape_exprs), True)
    write_frame_table(observations, *fit_rules(observations))


if __name__ == "__main__":
    main()
