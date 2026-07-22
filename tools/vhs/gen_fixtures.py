#!/usr/bin/env python3
"""Generate synthetic provider transcripts so Tokenhawk's own parsers populate a
demo index for README screenshots. No real session data is touched."""
import json, os, sys, shutil, datetime, hashlib

FIX = sys.argv[1]
now = datetime.datetime.now(datetime.timezone.utc)

def ts(minutes_ago):
    return (now - datetime.timedelta(minutes=minutes_ago)).isoformat().replace("+00:00", "Z")

def uid(seed):
    h = hashlib.sha1(seed.encode()).hexdigest()
    return f"{h[:8]}-{h[8:12]}-{h[12:16]}-{h[16:20]}-{h[20:32]}"

def backdate(path, minutes_ago):
    # finalize() clamps a session's UpdatedAt up to the file mtime, so the mtime
    # is what actually drives the "Updated" column and active/inactive split.
    when = (now - datetime.timedelta(minutes=minutes_ago)).timestamp()
    os.utime(path, (when, when))

def write_jsonl(path, records, minutes_ago=None):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w") as f:
        for r in records:
            f.write(json.dumps(r) + "\n")
    if minutes_ago is not None:
        backdate(path, minutes_ago)

# reset fixture tree
if os.path.isdir(FIX):
    shutil.rmtree(FIX)

# ---- Claude sessions -------------------------------------------------------
# fields: project, model, input_tokens, cache_read, cache_create, output, age_min, subagents
claude = [
    # active session with running + finished subagents (this is the detail shot)
    ("payments-core", "claude-opus-4-8", 42000, 1_520_000, 96000, 58_300, 1,
        [("Explore", "claude-sonnet-5", 8000, 210_000, 4_400, 12_100, 1, "running"),
         ("code-reviewer", "claude-sonnet-5", 5200, 96_000, 2_100, 7_800, 14, "completed")]),
    ("checkout-flow", "claude-sonnet-5", 6900, 1_040_000, 51_000, 44_600, 2,
        [("Explore", "claude-sonnet-5", 4100, 88_000, 1_900, 6_200, 2, "running")]),
    # low-cache alarm session (>=100k input, <80% cached) -> red row
    ("data-pipeline", "claude-opus-4-8", 61_000, 86_500, 12_000, 21_400, 18, []),
    ("web-dashboard", "claude-sonnet-5", 3200, 640_000, 28_000, 39_100, 42, []),
    ("search-index", "claude-opus-4-8", 12_000, 1_120_000, 44_000, 52_700, 95, []),
    ("notifications", "claude-haiku-4-5-20251001", 2100, 128_000, 6_400, 18_900, 140, []),
    ("infra-terraform", "claude-sonnet-5", 4800, 331_000, 15_000, 22_400, 320, []),
    ("docs-site", "claude-sonnet-5", 1800, 92_000, 3_100, 11_600, 610, []),
    ("billing-service", "claude-opus-4-8", 22_000, 878_000, 36_000, 40_300, 1300, []),
    ("ml-training", "claude-sonnet-5", 9200, 425_000, 19_000, 2_400, 2600, []),
]
for project, model, inp, cread, ccreate, out, age, subs in claude:
    cwd = f"/home/dev/src/{project}"
    sid = uid("claude" + project)
    base = os.path.join(FIX, "claude", project.replace("-", "_"))
    recs = [
        {"type": "user", "sessionId": sid, "cwd": cwd, "timestamp": ts(age + 30)},
        {"type": "assistant", "sessionId": sid, "cwd": cwd, "timestamp": ts(age),
         "message": {"model": model, "usage": {
             "input_tokens": inp, "cache_read_input_tokens": cread,
             "cache_creation_input_tokens": ccreate, "output_tokens": out}}},
    ]
    write_jsonl(os.path.join(base, sid + ".jsonl"), recs, age)
    for i, (name, smodel, sinp, scread, sccreate, sout, sage, status) in enumerate(subs):
        aid = uid(f"sub{project}{i}")
        sub_path = os.path.join(base, "subagents", f"agent-{aid}.jsonl")
        write_jsonl(sub_path, [
            {"type": "assistant", "sessionId": sid, "agentId": aid, "timestamp": ts(sage),
             "message": {"model": smodel, "usage": {
                 "input_tokens": sinp, "cache_read_input_tokens": scread,
                 "cache_creation_input_tokens": sccreate, "output_tokens": sout}}},
        ], sage)
        with open(sub_path[:-6] + ".meta.json", "w") as f:
            json.dump({"agentType": name}, f)

# ---- Codex sessions --------------------------------------------------------
# fields: project, model, input, cached, output, reasoning, total, age_min
codex = [
    ("acme-api", "gpt-5.6-sol", 43_222_400, 41_890_000, 207_600, 64_300, 43_430_000, 12),
    ("search-index", "gpt-5.3-codex", 22_763_800, 22_330_000, 76_200, 16_900, 22_840_000, 55),
    ("infra-terraform", "gpt-5.6-sol", 4_291_600, 4_120_000, 18_400, 9_200, 4_310_000, 900),
]
for project, model, inp, cached, out, reason, total, age in codex:
    cwd = f"/home/dev/src/{project}"
    sid = uid("codex" + project)
    path = os.path.join(FIX, "codex", "sessions", "2026", "07", "15", sid + ".jsonl")
    write_jsonl(path, [
        {"type": "session_meta", "timestamp": ts(age + 20),
         "payload": {"id": sid, "cwd": cwd, "timestamp": ts(age + 20)}},
        {"type": "turn_context", "payload": {"model": model, "cwd": cwd}},
        {"type": "event_msg", "timestamp": ts(age), "payload": {"type": "token_count",
         "info": {"total_token_usage": {
             "input_tokens": inp, "cached_input_tokens": cached, "output_tokens": out,
             "reasoning_output_tokens": reason, "total_tokens": total}}}},
    ], age)

# ---- Pi sessions (reported cost) ------------------------------------------
# fields: project, provider, model, input, output, cacheRead, cacheWrite, total, cost, age
pi = [
    ("acme-web", "anthropic", "claude-sonnet-4-5", 26_100, 737, 0, 4_200, 26_800, 0.1524, 70),
    ("payments-core", "openai", "codex/gpt-5.3", 2, 2, 0, 0, 2_200, 0.0137, 130),
    ("checkout-flow", "anthropic", "claude-sonnet-4-5", 2_300, 1_100, 2_200, 900, 9_300, 0.0665, 340),
]
for project, prov, model, inp, out, cread, cwrite, total, cost, age in pi:
    cwd = f"/home/dev/src/{project}"
    sid = uid("pi" + project)
    path = os.path.join(FIX, "pi", sid + ".jsonl")
    write_jsonl(path, [
        {"type": "session", "id": sid, "cwd": cwd, "timestamp": ts(age + 15)},
        {"type": "message", "timestamp": ts(age), "message": {
            "role": "assistant", "provider": prov, "model": model, "usage": {
                "input": inp, "output": out, "cacheRead": cread, "cacheWrite": cwrite,
                "totalTokens": total, "cost": {"total": cost}}}},
    ], age)

# ---- Gemini session --------------------------------------------------------
project = "web-dashboard"
cwd = f"/home/dev/src/{project}"
sid = uid("gemini" + project)
gdir = os.path.join(FIX, "gemini", "abc123hash")
os.makedirs(os.path.join(gdir, "chats"), exist_ok=True)
with open(os.path.join(gdir, ".project_root"), "w") as f:
    f.write(cwd)
with open(os.path.join(gdir, "chats", "session-1.json"), "w") as f:
    json.dump({"sessionId": sid, "startTime": ts(200), "lastUpdated": ts(160),
               "messages": [{"model": "gemini-3-pro-preview", "tokens": {
                   "Input": 148_000, "Cached": 92_000, "Output": 6_800,
                   "Thoughts": 3_400, "Tool": 1_200, "Total": 159_400}}]}, f)
backdate(os.path.join(gdir, "chats", "session-1.json"), 160)

print("fixtures written to", FIX)
