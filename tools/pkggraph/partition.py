"""Propose an acyclic package split from a pkggraph JSON graph.

Run with uv so the graph libraries are available:

    uv run --with networkx --with igraph --with leidenalg \
        tools/pkggraph/partition.py /tmp/server-graph.json --out /tmp/plan.json

Model:
- The hub type (a god object such as the server struct) and its fields stay
  in the root package. So does every declaration that names the hub type
  directly (other than as a method receiver), plus everything that depends
  on those: the root imports all other packages, so nothing below may depend
  on it.
- Hub methods are movable. Each hub field a moved method reads becomes an
  injected dependency of its new package; calls to hub methods that land in
  another package become cross-package calls.
- Remaining declarations are clustered with Leiden on the reference graph
  mixed with semantic affinity (same file, same topic word), so packages
  follow product areas as well as call structure.
- Go forbids import cycles. Where clusters form a cycle, the declarations
  that other members of the cycle reference move into a shared base package
  together with everything they depend on; base therefore never depends on a
  feature package. Small clusters then merge into the neighbour they reference
  most when that keeps the package graph acyclic.
- Each in-package test goes to the package holding the most code it reaches.
"""

import argparse
import collections
import json
import re

import igraph as ig
import leidenalg
import networkx as nx

TOPICS = [
    "settings", "activity", "workspace", "worktree", "runtime", "tmux", "terminal",
    "provider", "roborev", "federation", "fleet", "spoke", "hub", "mcp",
    "notification", "docs", "kata", "repo", "pull", "issue", "config", "auth",
    "token", "host", "devbox", "archive", "telemetry", "sync", "project", "mutation",
    "operation", "startup", "shutdown", "event", "stack", "review", "merge", "label",
    "comment", "browser", "login", "compression", "otel", "spa", "route", "huma",
    "clone", "filesystem", "agent", "session", "pty", "import",
]
ROOT, BASE = "root", "base"


def words(text):
    out = []
    for p in re.split(r"[_\W]+", text):
        out += re.findall(r"[A-Z]+(?=[A-Z][a-z]|\d|$)|[A-Z]?[a-z]+|\d+", p)
    return [w.lower() for w in out if w]


def topic(n):
    for w in words(n["file"].removesuffix(".go").removesuffix("_test")) + words(n["name"]):
        for t in TOPICS:
            if w.startswith(t):
                return t
    return None


class Model:
    def __init__(self, path, hub):
        g = json.load(open(path))
        self.hub = hub
        self.nodes = {n["id"]: n for n in g["nodes"]}
        self.edges = g["edges"]
        # Units: a non-hub type absorbs its methods and fields.
        self.owner = {}
        for nid, n in self.nodes.items():
            if n["kind"] in ("method", "field") and n["recv"] != hub and ("type:" + n["recv"]) in self.nodes:
                self.owner[nid] = "type:" + n["recv"]
            else:
                self.owner[nid] = nid
        self.lines = collections.Counter()
        for nid, u in self.owner.items():
            if self.nodes[nid]["kind"] in ("func", "method", "type", "var", "const"):
                self.lines[u] += self.nodes[nid]["lines"]
        self.is_test = {u: self.nodes[u]["test"] for u in set(self.owner.values()) if u in self.nodes}
        self.hubunits = {u for u in self.is_test if u == "type:" + hub or u.startswith("field:" + hub + ".")}
        self.dg = nx.DiGraph()
        for u, t in self.is_test.items():
            if not t and u not in self.hubunits:
                self.dg.add_node(u)
        self.test_refs = collections.defaultdict(collections.Counter)
        self.hub_field_refs = collections.defaultdict(set)
        self.direct_hub = set()
        for e in self.edges:
            a, b = self.owner.get(e["from"]), self.owner.get(e["to"])
            if a is None or b is None or a == b:
                continue
            if self.is_test.get(a):
                if not self.is_test.get(b):
                    self.test_refs[a][b] += e["weight"]
                continue
            if b == "type:" + hub and not a.startswith("method:" + hub + "."):
                self.direct_hub.add(a)
            if b.startswith("field:" + hub + "."):
                self.hub_field_refs[a].add(b.split(".", 1)[1])
            if a in self.dg and b in self.dg:
                w = self.dg.get_edge_data(a, b, {"w": 0})["w"]
                self.dg.add_edge(a, b, w=w + e["weight"])

    def root_units(self, pins=()):
        """Units that must stay in the root package: direct users of the hub
        type, pinned units (exported hub methods, root-owned API types), hub
        methods reading a hub field whose type lives in root, and everything
        that depends on any of those."""
        field_types = collections.defaultdict(set)
        for e in self.edges:
            if e["from"].startswith("field:" + self.hub + ".") and e["to"] != "type:" + self.hub:
                field_types[e["from"].split(".", 1)[1]].add(self.owner.get(e["to"], e["to"]))
        root = set(self.direct_hub & set(self.dg)) | {p for p in pins if p in self.dg}
        for u in self.dg:
            n = self.nodes[u]
            if n["kind"] == "method" and n["recv"] == self.hub and n["exported"]:
                root.add(u)
        while True:
            closure = set(root)
            for u in root:
                closure |= nx.ancestors(self.dg, u)
            for u, fs in self.hub_field_refs.items():
                if u in self.dg and any(field_types[f] & closure for f in fs):
                    closure.add(u)
            if closure == root:
                return root
            root = closure

    def affinity_graph(self, units, w_file, w_topic):
        g = nx.Graph()
        for u in units:
            g.add_node(u, lines=max(1, self.lines[u]))
        for a, b, d in self.dg.edges(data=True):
            if a in g and b in g:
                old = g.get_edge_data(a, b, {"weight": 0})["weight"]
                g.add_edge(a, b, weight=old + d["w"])
        by_file, by_topic = collections.defaultdict(set), collections.defaultdict(set)
        for u in units:
            n = self.nodes[u]
            by_file[n["file"]].add(u)
            t = topic(n)
            if t:
                by_topic[t].add(u)
        for groups, w in ((by_file, w_file), (by_topic, w_topic)):
            for members in groups.values():
                if len(members) < 2:
                    continue
                anchor = max(members, key=lambda m: g.nodes[m]["lines"])
                for m in members:
                    if m != anchor:
                        old = g.get_edge_data(m, anchor, {"weight": 0})["weight"]
                        g.add_edge(m, anchor, weight=old + w)
        return g


def leiden(g, resolution, seed):
    ig_g = ig.Graph.from_networkx(g)
    part = leidenalg.find_partition(
        ig_g, leidenalg.CPMVertexPartition, weights="weight", resolution_parameter=resolution,
        seed=seed, node_sizes=[g.nodes[n]["lines"] for n in ig_g.vs["_nx_name"]],
    )
    return {ig_g.vs[i]["_nx_name"]: f"c{c}" for i, c in enumerate(part.membership)}


def is_hub_call(m, b):
    return b.startswith("method:" + m.hub + ".")


def quotient(m, member):
    """Package import graph. Calls to hub methods in another package are
    wired through the root package, so they are not imports."""
    q = nx.DiGraph()
    q.add_nodes_from(set(member.values()))
    for a, b in m.dg.edges():
        pa, pb = member.get(a), member.get(b)
        if pa and pb and pa != pb and not (is_hub_call(m, b) and pa != ROOT):
            q.add_edge(pa, pb)
    return q


def import_descendants(m, u):
    """Declarations u needs through imports (hub-method calls excluded)."""
    seen, stack = set(), [u]
    while stack:
        x = stack.pop()
        for y in m.dg.successors(x):
            if y not in seen and not is_hub_call(m, y):
                seen.add(y)
                stack.append(y)
    return seen


def repair_cycles(m, member, base=BASE):
    """Push cycle-forming targets (and their dependencies) into base."""
    while True:
        q = quotient(m, member)
        sccs = [c for c in nx.strongly_connected_components(q) if len(c) > 1 and ROOT not in c]
        if not sccs:
            return
        moved = set()
        for scc in sccs:
            for a, b in m.dg.edges():
                pa, pb = member.get(a), member.get(b)
                if pa in scc and pb in scc and pa != pb and pb not in (base, ROOT) and not is_hub_call(m, b):
                    moved.add(b)
        closure = set(moved)
        for u in moved:
            closure |= {d for d in import_descendants(m, u) if member.get(d) != base}
        for u in closure:
            if member.get(u) != ROOT:
                member[u] = base


def merge_small(m, member, min_lines):
    """Merge clusters below min_lines into their most-referenced neighbour
    when that keeps the package graph acyclic."""
    changed = True
    while changed:
        changed = False
        sizes = collections.Counter()
        for u, p in member.items():
            sizes[p] += m.lines[u]
        for p in sorted(sizes, key=lambda p: sizes[p]):
            if p == ROOT or sizes[p] >= min_lines:
                continue
            links = collections.Counter()
            for a, b, d in m.dg.edges(data=True):
                pa, pb = member.get(a), member.get(b)
                if pa == p and pb != p:
                    links[pb] += d["w"]
                elif pb == p and pa != p:
                    links[pa] += d["w"]
            for target, _ in links.most_common():
                if target == ROOT:
                    continue
                trial = {u: (target if q == p else q) for u, q in member.items()}
                if nx.is_directed_acyclic_graph(quotient(m, trial)):
                    member.clear()
                    member.update(trial)
                    changed = True
                    break
            if changed:
                break


def report(m, member, names=None):
    sizes, tests = collections.Counter(), collections.Counter()
    for u, p in member.items():
        sizes[p] += m.lines[u]
    test_home = {}
    for t, refs in m.test_refs.items():
        score = collections.Counter()
        for u, w in refs.items():
            score[member.get(u, ROOT)] += w
        # Root holds constructors and harness code every test touches; a
        # test belongs with the feature code it exercises.
        feature = [(p, w) for p, w in score.most_common() if p != ROOT]
        home = feature[0][0] if feature else ROOT
        test_home[t] = home
        tests[home] += m.lines[t]
    q = quotient(m, member)
    fields = collections.defaultdict(set)
    for u, fs in m.hub_field_refs.items():
        p = member.get(u, ROOT)
        if p != ROOT:
            fields[p] |= fs
    exports, hubcalls = collections.defaultdict(set), collections.Counter()
    for a, b in m.dg.edges():
        pa, pb = member.get(a), member.get(b)
        if pa != pb and pa and pb:
            if not m.nodes[b]["exported"]:
                exports[pb].add(b)
            if is_hub_call(m, b) and pa != ROOT:
                hubcalls[pa] += 1
    print(f"packages: {len(sizes)}, acyclic: {nx.is_directed_acyclic_graph(q)}, "
          f"prod lines {sum(sizes.values())}, test lines {sum(tests.values())}")
    print(f"{'package':34} {'prod':>6} {'tests':>6} {'fields':>6} {'xhub':>5} {'exports':>7}  depends on")
    for p in sorted(sizes, key=lambda p: -(sizes[p] + tests[p])):
        label = (names or {}).get(p, p)
        deps = ",".join(sorted((names or {}).get(d, d) for d in q.successors(p)))
        print(f"{label:34} {sizes[p]:6} {tests[p]:6} {len(fields[p]):6} {hubcalls[p]:5} {len(exports[p]):7}  {deps}")
    return test_home, fields, exports


def name_clusters(m, member):
    names = {ROOT: ROOT}
    used = collections.Counter()
    for p in set(member.values()):
        if p in names:
            continue
        count = collections.Counter()
        for u, q in member.items():
            if q == p:
                count[topic(m.nodes[u]) or "misc"] += m.lines[u]
        top = count.most_common(1)[0][0]
        used[top] += 1
        names[p] = top if used[top] == 1 else f"{top}{used[top]}"
    return names


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("graph")
    ap.add_argument("--hub", default="Server")
    ap.add_argument("--resolution", type=float, default=0.002)
    ap.add_argument("--w-file", type=float, default=20)
    ap.add_argument("--w-topic", type=float, default=10)
    ap.add_argument("--min-lines", type=int, default=400)
    ap.add_argument("--seed", type=int, default=1)
    ap.add_argument("--max-layers", type=int, default=4)
    ap.add_argument("--pins", help="file of unit ids that must stay in the root package")
    ap.add_argument("--out")
    args = ap.parse_args()
    m = Model(args.graph, args.hub)
    pins = [p.strip() for p in open(args.pins)] if args.pins else []
    root = m.root_units([p for p in pins if p])
    rest = [u for u in m.dg if u not in root]
    member = {u: ROOT for u in root}
    member.update(leiden(m.affinity_graph(rest, args.w_file, args.w_topic), args.resolution, args.seed))
    repair_cycles(m, member, "base0")
    # Layer the shared base: re-cluster it by topic and push any cycle it
    # forms one layer further down, until the bottom layer is small.
    for level in range(args.max_layers):
        layer = [u for u, p in member.items() if p == f"base{level}"]
        if sum(m.lines[u] for u in layer) <= args.min_lines * 2:
            break
        sub = leiden(m.affinity_graph(layer, args.w_file, args.w_topic), args.resolution, args.seed)
        for u in layer:
            member[u] = f"L{level}" + sub[u]
        repair_cycles(m, member, f"base{level + 1}")
    merge_small(m, member, args.min_lines)
    names = name_clusters(m, member)
    test_home, fields, exports = report(m, member, names)
    if args.out:
        json.dump({
            "resolution": args.resolution,
            "packages": {u: names[p] for u, p in member.items()},
            "tests": {t: names.get(p, p) for t, p in test_home.items()},
            "injected_fields": {names[p]: sorted(f) for p, f in fields.items()},
            "exports": {names[p]: sorted(e) for p, e in exports.items()},
        }, open(args.out, "w"), indent=1)
        with open(args.out, "a") as f:
            f.write("\n")


if __name__ == "__main__":
    main()
