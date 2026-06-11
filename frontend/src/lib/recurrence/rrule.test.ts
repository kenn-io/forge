import { describe, expect, test } from "vite-plus/test";
import { formatRRule, parseRRule, serializeRRule } from "./rrule";
import type { CommonRule } from "./rrule";
import {
  ADVANCED_FIXTURES,
  ADVANCED_LAST_WEEKDAY,
  ADVANCED_MIXED_MONTHLY,
  ADVANCED_TIME_OF_DAY,
} from "./__fixtures__/advancedRules";

describe("parseRRule — invalid", () => {
  test.each([
    ["missing FREQ", "INTERVAL=2;COUNT=5"],
    ["unknown FREQ value", "FREQ=FORTNIGHTLY"],
    ["empty FREQ", "FREQ="],
    ["duplicate keys", "FREQ=DAILY;FREQ=WEEKLY"],
    ["malformed UNTIL with dashes", "FREQ=DAILY;UNTIL=2026-05-20"],
    ["malformed UNTIL non-digits", "FREQ=DAILY;UNTIL=YESTERDAY"],
    ["impossible UNTIL date", "FREQ=DAILY;UNTIL=20261340"],
    ["impossible UNTIL time", "FREQ=DAILY;UNTIL=20260520T246060Z"],
    ["malformed BYDAY", "FREQ=WEEKLY;BYDAY=XX"],
    ["BYDAY ordinal 0", "FREQ=WEEKLY;BYDAY=0MO"],
    ["unknown property", "FREQ=DAILY;FOOBAR=1"],
    ["RDATE inside RRULE", "FREQ=DAILY;RDATE=20260520"],
    ["malformed segment (no equals)", "FREQ=DAILY;BROKEN"],
    ["INTERVAL=0", "FREQ=DAILY;INTERVAL=0"],
    ["INTERVAL negative", "FREQ=DAILY;INTERVAL=-1"],
    ["INTERVAL non-numeric", "FREQ=DAILY;INTERVAL=two"],
    ["COUNT=0", "FREQ=DAILY;COUNT=0"],
    ["DAILY BYMONTHDAY=0", "FREQ=DAILY;BYMONTHDAY=0"],
    ["WEEKLY BYMONTH=13", "FREQ=WEEKLY;BYMONTH=13"],
    ["MONTHLY BYMONTHDAY=0", "FREQ=MONTHLY;BYMONTHDAY=0"],
    ["YEARLY BYMONTH=3;BYMONTHDAY=0", "FREQ=YEARLY;BYMONTH=3;BYMONTHDAY=0"],
    ["BYMONTHDAY=32 out of range", "FREQ=MONTHLY;BYMONTHDAY=32"],
    ["BYMONTHDAY=-32 out of range", "FREQ=MONTHLY;BYMONTHDAY=-32"],
    ["BYMONTHDAY non-integer", "FREQ=MONTHLY;BYMONTHDAY=abc"],
    ["BYHOUR non-integer", "FREQ=DAILY;BYHOUR=abc"],
    ["BYHOUR out of range", "FREQ=DAILY;BYHOUR=24"],
    ["BYHOUR empty", "FREQ=DAILY;BYHOUR="],
    ["BYMINUTE out of range", "FREQ=DAILY;BYMINUTE=60"],
    ["BYSECOND out of range", "FREQ=DAILY;BYSECOND=99"],
    ["BYSECOND=61 above leap-second cap", "FREQ=DAILY;BYSECOND=61"],
    ["BYSETPOS=0 invalid", "FREQ=MONTHLY;BYDAY=MO;BYSETPOS=0"],
    ["BYSETPOS out of range", "FREQ=MONTHLY;BYDAY=MO;BYSETPOS=400"],
    ["BYWEEKNO=54 out of range", "FREQ=YEARLY;BYWEEKNO=54"],
    ["BYWEEKNO=0 invalid", "FREQ=YEARLY;BYWEEKNO=0"],
    ["BYYEARDAY=0 invalid", "FREQ=YEARLY;BYYEARDAY=0"],
    ["BYYEARDAY out of range", "FREQ=YEARLY;BYYEARDAY=400"],
    ["WKST=XX unknown weekday", "FREQ=WEEKLY;WKST=XX"],
    ["WKST empty", "FREQ=WEEKLY;WKST="],
    ["empty segment (trailing semicolon)", "FREQ=DAILY;"],
    ["empty segment (doubled semicolon)", "FREQ=DAILY;;COUNT=2"],
    ["empty segment (leading semicolon)", ";FREQ=DAILY"],
    ["BYHOUR list with one bad token", "FREQ=DAILY;BYHOUR=9,99"],
    ["BYSETPOS list with empty token", "FREQ=MONTHLY;BYDAY=MO;BYSETPOS=1,,2"],
    ["malformed key after a well-formed one", "FREQ=DAILY;BYHOUR=9;BYMINUTE=99"],
    ["sub-daily FREQ with malformed BYHOUR", "FREQ=HOURLY;BYHOUR=abc"],
    ["sub-daily FREQ with unknown WKST", "FREQ=MINUTELY;WKST=XX"],
    ["BYMONTHDAY list with empty token", "FREQ=MONTHLY;BYMONTHDAY=1,,15"],
    ["BYMONTHDAY list with out-of-range token", "FREQ=MONTHLY;BYMONTHDAY=1,32"],
    ["BYMONTHDAY list with zero token", "FREQ=MONTHLY;BYMONTHDAY=1,0"],
  ])("%s -> invalid", (_label, input) => {
    const result = parseRRule(input);
    expect(result.kind).toBe("invalid");
    if (result.kind === "invalid") {
      expect(result.raw).toBe(input);
      expect(result.error).toBeTruthy();
    }
  });
});

describe("parseRRule — minimal common (FREQ only)", () => {
  test.each([
    ["FREQ=DAILY", "DAILY"],
    ["FREQ=WEEKLY", "WEEKLY"],
    ["FREQ=MONTHLY", "MONTHLY"],
    ["FREQ=YEARLY", "YEARLY"],
  ] as const)("%s -> common", (input, freq) => {
    const result = parseRRule(input);
    expect(result.kind).toBe("common");
    if (result.kind === "common") {
      expect(result.rrule.freq).toBe(freq);
      expect(result.rrule.interval).toBe(1);
    }
  });

  test("FREQ=DAILY;INTERVAL=3 -> common, interval 3", () => {
    const result = parseRRule("FREQ=DAILY;INTERVAL=3");
    expect(result.kind).toBe("common");
    if (result.kind === "common") {
      expect(result.rrule.interval).toBe(3);
    }
  });
});

describe("parseRRule — common (extended)", () => {
  test("WEEKLY;BYDAY=MO -> common with byDay [MO]", () => {
    const r = parseRRule("FREQ=WEEKLY;BYDAY=MO");
    expect(r.kind).toBe("common");
    if (r.kind === "common") expect(r.rrule.byDay).toEqual(["MO"]);
  });

  test("WEEKLY;BYDAY=MO,WE,FR -> common with byDay [MO,WE,FR] in canonical order", () => {
    const r = parseRRule("FREQ=WEEKLY;BYDAY=FR,MO,WE");
    expect(r.kind).toBe("common");
    if (r.kind === "common") expect(r.rrule.byDay).toEqual(["MO", "WE", "FR"]);
  });

  test("MONTHLY;BYMONTHDAY=15 -> common dayOfMonth 15", () => {
    const r = parseRRule("FREQ=MONTHLY;BYMONTHDAY=15");
    expect(r.kind).toBe("common");
    if (r.kind === "common") {
      expect(r.rrule.dayInMonth).toEqual({ kind: "dayOfMonth", day: 15 });
    }
  });

  test("MONTHLY;BYMONTHDAY=-1 -> common dayOfMonth -1 (last)", () => {
    const r = parseRRule("FREQ=MONTHLY;BYMONTHDAY=-1");
    expect(r.kind).toBe("common");
    if (r.kind === "common") {
      expect(r.rrule.dayInMonth).toEqual({ kind: "dayOfMonth", day: -1 });
    }
  });

  test("MONTHLY;BYDAY=1MO -> common nthWeekday 1st Monday", () => {
    const r = parseRRule("FREQ=MONTHLY;BYDAY=1MO");
    expect(r.kind).toBe("common");
    if (r.kind === "common") {
      expect(r.rrule.dayInMonth).toEqual({ kind: "nthWeekday", ordinal: 1, weekday: "MO" });
    }
  });

  test("MONTHLY;BYDAY=-1FR -> common nthWeekday last Friday", () => {
    const r = parseRRule("FREQ=MONTHLY;BYDAY=-1FR");
    expect(r.kind).toBe("common");
    if (r.kind === "common") {
      expect(r.rrule.dayInMonth).toEqual({ kind: "nthWeekday", ordinal: -1, weekday: "FR" });
    }
  });

  test("YEARLY;BYMONTH=3 -> common byMonth 3, no dayInMonth", () => {
    const r = parseRRule("FREQ=YEARLY;BYMONTH=3");
    expect(r.kind).toBe("common");
    if (r.kind === "common") {
      expect(r.rrule.byMonth).toBe(3);
      expect(r.rrule.dayInMonth).toBeUndefined();
    }
  });

  test("YEARLY;BYMONTH=3;BYMONTHDAY=1 -> common (March 1st)", () => {
    const r = parseRRule("FREQ=YEARLY;BYMONTH=3;BYMONTHDAY=1");
    expect(r.kind).toBe("common");
    if (r.kind === "common") {
      expect(r.rrule.byMonth).toBe(3);
      expect(r.rrule.dayInMonth).toEqual({ kind: "dayOfMonth", day: 1 });
    }
  });

  test("YEARLY;BYMONTH=3;BYMONTHDAY=-1 -> advanced (no yearly last-day affordance)", () => {
    // The Yearly form has no "last day of month" checkbox (only Monthly
    // does), so a yearly last-day rule must round-trip through Advanced
    // — otherwise applyCommonRuleToForm would set dayInMonthLastDay=true
    // but the Yearly builder ignores it and saves BYMONTHDAY=1.
    const r = parseRRule("FREQ=YEARLY;BYMONTH=3;BYMONTHDAY=-1");
    expect(r.kind).toBe("advanced");
  });

  test("YEARLY;BYMONTH=3;BYMONTHDAY=-01 -> advanced (equivalent to -1)", () => {
    // BYMONTHDAY integers can carry a leading sign and zero-pad; -01
    // parses identically to -1 per the RFC, so the yearly last-day
    // round-trip guard must match the parsed value, not the raw string.
    const r = parseRRule("FREQ=YEARLY;BYMONTH=3;BYMONTHDAY=-01");
    expect(r.kind).toBe("advanced");
  });

  test("YEARLY;BYMONTH=3;BYDAY=1MO -> common (1st Monday of March)", () => {
    const r = parseRRule("FREQ=YEARLY;BYMONTH=3;BYDAY=1MO");
    expect(r.kind).toBe("common");
    if (r.kind === "common") {
      expect(r.rrule.dayInMonth).toEqual({ kind: "nthWeekday", ordinal: 1, weekday: "MO" });
    }
  });

  test("UNTIL=YYYYMMDD -> common with until end", () => {
    const r = parseRRule("FREQ=DAILY;UNTIL=20260520");
    expect(r.kind).toBe("common");
    if (r.kind === "common") {
      expect(r.rrule.end).toEqual({ kind: "until", date: "2026-05-20" });
    }
  });

  test("COUNT=10 -> common with count end", () => {
    const r = parseRRule("FREQ=WEEKLY;COUNT=10");
    expect(r.kind).toBe("common");
    if (r.kind === "common") {
      expect(r.rrule.end).toEqual({ kind: "count", count: 10 });
    }
  });

  test("WEEKLY;INTERVAL=2;BYDAY=MO,FR -> common with interval 2 and byDay MO,FR", () => {
    const r = parseRRule("FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,FR");
    expect(r.kind).toBe("common");
    if (r.kind === "common") {
      expect(r.rrule.interval).toBe(2);
      expect(r.rrule.byDay).toEqual(["MO", "FR"]);
    }
  });

  test("MONTHLY;INTERVAL=3;BYMONTHDAY=15 -> common with interval 3", () => {
    const r = parseRRule("FREQ=MONTHLY;INTERVAL=3;BYMONTHDAY=15");
    expect(r.kind).toBe("common");
    if (r.kind === "common") {
      expect(r.rrule.interval).toBe(3);
      expect(r.rrule.dayInMonth).toEqual({ kind: "dayOfMonth", day: 15 });
    }
  });
});

describe("parseRRule — advanced classification", () => {
  test.each([
    ["sub-daily FREQ HOURLY", "FREQ=HOURLY;COUNT=24"],
    ["sub-daily FREQ MINUTELY", "FREQ=MINUTELY;INTERVAL=15"],
    ["sub-daily FREQ SECONDLY", "FREQ=SECONDLY;COUNT=5"],
    ["BYSETPOS last weekday", ADVANCED_LAST_WEEKDAY],
    ["BYHOUR + BYMINUTE", ADVANCED_TIME_OF_DAY],
    ["BYWEEKNO", "FREQ=WEEKLY;BYWEEKNO=12"],
    ["BYYEARDAY", "FREQ=YEARLY;BYYEARDAY=100"],
    ["WKST on WEEKLY", "FREQ=WEEKLY;WKST=MO"],
    ["MONTHLY BYDAY no ordinal", "FREQ=MONTHLY;BYDAY=MO,TU,WE,TH,FR"],
    ["MONTHLY mixed BYMONTHDAY+BYDAY", ADVANCED_MIXED_MONTHLY],
    ["YEARLY BYMONTH+BYMONTHDAY+BYDAY", "FREQ=YEARLY;BYMONTH=3;BYMONTHDAY=15;BYDAY=FR"],
    ["YEARLY BYMONTHDAY without BYMONTH", "FREQ=YEARLY;BYMONTHDAY=15"],
    ["UNTIL with time-of-day", "FREQ=DAILY;UNTIL=20260520T000000Z"],
    ["BYMONTH on MONTHLY", "FREQ=MONTHLY;BYMONTH=3"],
    ["BYMONTH on DAILY", "FREQ=DAILY;BYMONTH=3"],
    ["both COUNT and UNTIL", "FREQ=DAILY;COUNT=5;UNTIL=20260520"],
    ["BYMONTH on WEEKLY", "FREQ=WEEKLY;BYMONTH=3"],
    ["YEARLY BYDAY no ordinal", "FREQ=YEARLY;BYDAY=MO,TU"],
    ["BYSECOND standalone on DAILY", "FREQ=DAILY;BYSECOND=30"],
    ["YEARLY BYMONTHDAY=-1 has no Common-mode affordance", "FREQ=YEARLY;BYMONTH=3;BYMONTHDAY=-1"],
    ["BYMONTHDAY list is too rich for the form", "FREQ=MONTHLY;BYMONTHDAY=1,15"],
    ["BYMONTHDAY list with last-day token", "FREQ=MONTHLY;BYMONTHDAY=15,-1"],
  ])("%s -> advanced, raw preserved", (_label, input) => {
    const r = parseRRule(input);
    expect(r.kind).toBe("advanced");
    if (r.kind === "advanced") {
      expect(r.raw).toBe(input); // byte-for-byte
    }
  });

  test("ADVANCED_FIXTURES all classify as advanced", () => {
    for (const rule of ADVANCED_FIXTURES) {
      const r = parseRRule(rule);
      expect(r.kind).toBe("advanced");
      if (r.kind === "advanced") expect(r.raw).toBe(rule);
    }
  });
});

describe("serializeRRule", () => {
  test.each([
    [{ freq: "DAILY", interval: 1 }, "FREQ=DAILY;INTERVAL=1"],
    [{ freq: "DAILY", interval: 3 }, "FREQ=DAILY;INTERVAL=3"],
    [{ freq: "WEEKLY", interval: 2, byDay: ["MO", "WE", "FR"] }, "FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,WE,FR"],
    [
      { freq: "MONTHLY", interval: 1, dayInMonth: { kind: "dayOfMonth", day: 15 } },
      "FREQ=MONTHLY;INTERVAL=1;BYMONTHDAY=15",
    ],
    [
      { freq: "MONTHLY", interval: 1, dayInMonth: { kind: "dayOfMonth", day: -1 } },
      "FREQ=MONTHLY;INTERVAL=1;BYMONTHDAY=-1",
    ],
    [
      { freq: "MONTHLY", interval: 1, dayInMonth: { kind: "nthWeekday", ordinal: 1, weekday: "MO" } },
      "FREQ=MONTHLY;INTERVAL=1;BYDAY=1MO",
    ],
    [
      { freq: "MONTHLY", interval: 1, dayInMonth: { kind: "nthWeekday", ordinal: -1, weekday: "FR" } },
      "FREQ=MONTHLY;INTERVAL=1;BYDAY=-1FR",
    ],
    [{ freq: "YEARLY", interval: 1, byMonth: 3 }, "FREQ=YEARLY;INTERVAL=1;BYMONTH=3"],
    [
      { freq: "YEARLY", interval: 1, byMonth: 3, dayInMonth: { kind: "dayOfMonth", day: 1 } },
      "FREQ=YEARLY;INTERVAL=1;BYMONTH=3;BYMONTHDAY=1",
    ],
    [
      { freq: "YEARLY", interval: 1, byMonth: 3, dayInMonth: { kind: "nthWeekday", ordinal: 1, weekday: "MO" } },
      "FREQ=YEARLY;INTERVAL=1;BYMONTH=3;BYDAY=1MO",
    ],
    [{ freq: "DAILY", interval: 1, end: { kind: "count", count: 10 } }, "FREQ=DAILY;INTERVAL=1;COUNT=10"],
    [
      { freq: "DAILY", interval: 1, end: { kind: "until", date: "2026-05-20" } },
      "FREQ=DAILY;INTERVAL=1;UNTIL=20260520",
    ],
    [{ freq: "DAILY", interval: 1, end: { kind: "never" } }, "FREQ=DAILY;INTERVAL=1"],
  ] as Array<[CommonRule, string]>)("serializes %j", (rule, expected) => {
    expect(serializeRRule(rule)).toBe(expected);
  });

  test("WEEKLY byDay is sorted canonically regardless of input order", () => {
    const rule: CommonRule = { freq: "WEEKLY", interval: 1, byDay: ["FR", "MO", "WE"] };
    expect(serializeRRule(rule)).toBe("FREQ=WEEKLY;INTERVAL=1;BYDAY=MO,WE,FR");
  });
});

describe("round-trip invariants", () => {
  const commonInputs = [
    "FREQ=DAILY;INTERVAL=1",
    "FREQ=DAILY;INTERVAL=3",
    "FREQ=WEEKLY;INTERVAL=1;BYDAY=MO,WE,FR",
    "FREQ=MONTHLY;INTERVAL=1;BYMONTHDAY=15",
    "FREQ=MONTHLY;INTERVAL=1;BYMONTHDAY=-1",
    "FREQ=MONTHLY;INTERVAL=1;BYDAY=1MO",
    "FREQ=MONTHLY;INTERVAL=2;BYDAY=-1FR",
    "FREQ=YEARLY;INTERVAL=1;BYMONTH=3",
    "FREQ=YEARLY;INTERVAL=1;BYMONTH=3;BYMONTHDAY=1",
    "FREQ=YEARLY;INTERVAL=1;BYMONTH=3;BYDAY=1MO",
    "FREQ=DAILY;INTERVAL=1;COUNT=10",
    "FREQ=DAILY;INTERVAL=1;UNTIL=20260520",
  ];

  test.each(commonInputs)("serialize(parse(%s).rrule) === input (canonical form)", (input) => {
    const parsed = parseRRule(input);
    expect(parsed.kind).toBe("common");
    if (parsed.kind === "common") {
      expect(serializeRRule(parsed.rrule)).toBe(input);
    }
  });

  test.each(commonInputs)("parse(serialize(parse(%s))) structurally equals first parse", (input) => {
    const first = parseRRule(input);
    expect(first.kind).toBe("common");
    if (first.kind === "common") {
      const second = parseRRule(serializeRRule(first.rrule));
      expect(second).toEqual(first);
    }
  });

  test("advanced fixtures: raw is preserved byte-for-byte (parser does not normalize)", () => {
    for (const rule of ADVANCED_FIXTURES) {
      const r = parseRRule(rule);
      expect(r.kind).toBe("advanced");
      if (r.kind === "advanced") expect(r.raw).toBe(rule);
    }
  });

  // Non-canonical Common input should reparse to the same CommonRule
  // as its canonical form — even though re-serialising will normalise
  // the bytes (interval inserted, weekdays sorted, etc.).
  test.each([
    ["FREQ=WEEKLY;BYDAY=FR,MO", "FREQ=WEEKLY;INTERVAL=1;BYDAY=MO,FR"],
    ["FREQ=DAILY", "FREQ=DAILY;INTERVAL=1"],
  ])("non-canonical %s parses to same CommonRule as canonical %s", (a, b) => {
    const A = parseRRule(a);
    const B = parseRRule(b);
    expect(A.kind).toBe("common");
    expect(B.kind).toBe("common");
    if (A.kind === "common" && B.kind === "common") {
      expect(A.rrule).toEqual(B.rrule);
    }
  });
});

describe("formatRRule — common", () => {
  test.each([
    ["FREQ=DAILY;INTERVAL=1", "Daily"],
    ["FREQ=DAILY;INTERVAL=3", "Every 3 days"],
    ["FREQ=WEEKLY;INTERVAL=1;BYDAY=MO", "Weekly on Mon"],
    ["FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,WE,FR", "Every 2 weeks on Mon, Wed, Fri"],
    ["FREQ=MONTHLY;INTERVAL=1;BYMONTHDAY=15", "Monthly on day 15"],
    ["FREQ=MONTHLY;INTERVAL=1;BYMONTHDAY=-1", "Monthly on the last day"],
    ["FREQ=MONTHLY;INTERVAL=1;BYDAY=1MO", "Monthly on the 1st Monday"],
    ["FREQ=MONTHLY;INTERVAL=1;BYDAY=-1FR", "Monthly on the last Friday"],
    ["FREQ=YEARLY;INTERVAL=1;BYMONTH=3", "Yearly in March"],
    ["FREQ=YEARLY;INTERVAL=1;BYMONTH=3;BYMONTHDAY=1", "Yearly on March 1"],
    ["FREQ=YEARLY;INTERVAL=1;BYMONTH=3;BYDAY=1MO", "Yearly on the 1st Monday of March"],
    ["FREQ=DAILY;INTERVAL=1;COUNT=10", "Daily, 10 times"],
    ["FREQ=DAILY;INTERVAL=1;COUNT=1", "Daily, 1 time"],
    ["FREQ=DAILY;INTERVAL=1;UNTIL=20260520", "Daily until 2026-05-20"],
  ])("%s -> %s", (input, expected) => {
    expect(formatRRule(input)).toBe(expected);
  });
});

describe("formatRRule — advanced", () => {
  test("short advanced rule is shown verbatim under the prefix", () => {
    expect(formatRRule("FREQ=DAILY;BYHOUR=9")).toBe("Advanced: FREQ=DAILY;BYHOUR=9");
  });

  test("long advanced rule is truncated past 40 chars", () => {
    const long = "FREQ=MONTHLY;BYDAY=MO,TU,WE,TH,FR;BYSETPOS=-1";
    expect(formatRRule(long)).toBe(`Advanced: ${long.slice(0, 40)}…`);
  });

  test("advanced rule at exactly 40 chars: no truncation, no ellipsis", () => {
    const at40 = "FREQ=DAILY;BYHOUR=23;BYMINUTE=30;WKST=MO"; // 40 chars
    expect(at40.length).toBe(40);
    // Sanity: must classify as advanced; if it's invalid, formatRRule
    // would return "Invalid recurrence" and the truncation branch
    // would never execute.
    expect(parseRRule(at40).kind).toBe("advanced");
    expect(formatRRule(at40)).toBe(`Advanced: ${at40}`);
  });

  test("advanced rule at 41 chars: truncates first 40 + ellipsis", () => {
    const at41 = "FREQ=DAILY;BYHOUR=9;BYMINUTE=0;BYSECOND=0"; // 41 chars
    expect(at41.length).toBe(41);
    expect(parseRRule(at41).kind).toBe("advanced");
    expect(formatRRule(at41)).toBe(`Advanced: ${at41.slice(0, 40)}…`);
  });

  test("advanced rule at ~80 chars: truncation logic doesn't off-by-one for long inputs", () => {
    const long = "FREQ=DAILY;BYHOUR=9;BYMINUTE=0;BYSECOND=0;BYSETPOS=1;BYWEEKNO=12;BYYEARDAY=100";
    expect(parseRRule(long).kind).toBe("advanced");
    expect(formatRRule(long).startsWith("Advanced: ")).toBe(true);
    expect(formatRRule(long).endsWith("…")).toBe(true);
    expect(formatRRule(long).length).toBe(10 + 40 + 1); // "Advanced: " + 40 chars + "…"
  });
});

describe("formatRRule — invalid", () => {
  test("invalid rules return the fixed sentinel string", () => {
    expect(formatRRule("FREQ=FORTNIGHTLY")).toBe("Invalid recurrence");
    expect(formatRRule("")).toBe("Invalid recurrence");
  });
});

describe("formatRRule — determinism", () => {
  test.each([
    "FREQ=DAILY;INTERVAL=1",
    "FREQ=WEEKLY;BYDAY=MO,WE",
    "FREQ=MONTHLY;BYDAY=1MO",
    "FREQ=DAILY;BYHOUR=9",
    "FREQ=FORTNIGHTLY",
  ])("formatRRule(%s) is deterministic across calls", (input) => {
    expect(formatRRule(input)).toBe(formatRRule(input));
  });
});
