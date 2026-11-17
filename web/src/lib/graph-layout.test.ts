// The shared graph layout cases (spec/fixtures/graph-layout/README.md, written by scripts/gen/graph_layout.py):
// every field of every case, at a 1e-9 tolerance, so the web's copy of the rules (lib/graph-layout.ts) agrees with
// the reference to the bit. What draws a laid-out graph is views/extensions/ViewBody10.tsx.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { layOutGraph, type LayoutGraph } from "./graph-layout";

interface Case {
  name: string;
  graph: LayoutGraph;
  expected: {
    at: Record<string, [number, number]>;
    size: [number, number];
    edges: [string, string][];
    order: string[];
    depth: Record<string, number>;
    folds: Record<string, number>;
    frames: { group: string; members: string[] }[];
  };
}

const doc = JSON.parse(readFileSync(new URL("../../../spec/fixtures/graph-layout/cases.json", import.meta.url), "utf8")) as { version: number; cases: Case[] };

const near = (a: number, b: number): boolean => Math.abs(a - b) <= 1e-9;

describe("the shared graph layout cases", () => {
  it("is the cases file this test is written against", () => {
    expect(doc.version).toBe(1);
    expect(doc.cases.length).toBeGreaterThanOrEqual(19);
  });

  for (const c of doc.cases) {
    it(`lays out "${c.name}"`, () => {
      const got = layOutGraph(c.graph);
      const want = c.expected;
      expect(Object.keys(got.at).sort()).toEqual(Object.keys(want.at).sort());
      for (const id of Object.keys(want.at)) {
        expect(near(got.at[id][0], want.at[id][0]), `${id} x`).toBe(true);
        expect(near(got.at[id][1], want.at[id][1]), `${id} y`).toBe(true);
      }
      expect(got.size.map((v, i) => near(v, want.size[i]))).toEqual([true, true]);
      expect(got.edges).toEqual(want.edges);
      expect(got.order).toEqual(want.order);
      expect(got.depth).toEqual(want.depth);
      expect(got.folds).toEqual(want.folds);
      expect(got.frames).toEqual(want.frames);
    });
  }
});
