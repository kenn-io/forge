import { MAX_OSC52_CLIPBOARD_BYTES } from "./osc52Clipboard";

export interface Osc52OutputFilter {
  write(data: Uint8Array): Uint8Array;
  reset(): void;
}

interface Osc52OutputFilterOptions {
  maxDataBytes?: number;
}

type ParserState = "normal" | "escape" | "osc-command" | "osc52" | "osc52-escape";

const ESC = 0x1b;
const CAN = 0x18;
const BEL = 0x07;
const C1_OSC = 0x9d;
const C1_ST = 0x9c;
const BACKSLASH = 0x5c;
const SEMICOLON = 0x3b;
const DEFAULT_MAX_DATA_BYTES = 2 + Math.ceil(MAX_OSC52_CLIPBOARD_BYTES / 3) * 4;

export function createOsc52OutputFilter(
  onOsc52: (data: string) => void,
  options: Osc52OutputFilterOptions = {},
): Osc52OutputFilter {
  const maxDataBytes = options.maxDataBytes ?? DEFAULT_MAX_DATA_BYTES;
  let state: ParserState = "normal";
  let candidate: number[] = [];
  let command: number[] = [];
  let osc52Data: number[] = [];
  let discardOsc52 = false;

  function finishOsc52(): void {
    if (!discardOsc52) {
      onOsc52(new TextDecoder().decode(Uint8Array.from(osc52Data)));
    }
    osc52Data = [];
    discardOsc52 = false;
    state = "normal";
  }

  function rejectCandidate(output: number[], byte?: number): void {
    output.push(...candidate);
    candidate = [];
    command = [];
    if (byte !== undefined) output.push(byte);
    state = "normal";
  }

  return {
    write(data) {
      const output: number[] = [];

      for (const byte of data) {
        switch (state) {
          case "normal":
            if (byte === ESC) {
              state = "escape";
            } else if (byte === C1_OSC) {
              candidate = [byte];
              command = [];
              state = "osc-command";
            } else {
              output.push(byte);
            }
            break;

          case "escape":
            if (byte === 0x5d) {
              candidate = [ESC, byte];
              command = [];
              state = "osc-command";
            } else if (byte === C1_OSC) {
              output.push(ESC);
              candidate = [byte];
              command = [];
              state = "osc-command";
            } else {
              output.push(ESC);
              if (byte === ESC) {
                state = "escape";
              } else {
                output.push(byte);
                state = "normal";
              }
            }
            break;

          case "osc-command":
            if (byte === ESC) {
              output.push(...candidate);
              candidate = [];
              command = [];
              state = "escape";
            } else if (byte === C1_OSC) {
              output.push(...candidate);
              candidate = [byte];
              command = [];
            } else if (byte === SEMICOLON) {
              if (command.length === 2 && command[0] === 0x35 && command[1] === 0x32) {
                output.push(CAN);
                candidate = [];
                command = [];
                osc52Data = [];
                discardOsc52 = false;
                state = "osc52";
              } else {
                rejectCandidate(output, byte);
              }
            } else if (byte === BEL || byte === C1_ST) {
              output.push(...candidate, byte);
              candidate = [];
              command = [];
              state = "normal";
            } else {
              candidate.push(byte);
              command.push(byte);
              if (
                command.length > 2 ||
                (command.length === 1 && command[0] !== 0x35) ||
                (command.length === 2 && command[1] !== 0x32)
              ) {
                rejectCandidate(output);
              }
            }
            break;

          case "osc52":
            if (byte === BEL || byte === C1_ST) {
              finishOsc52();
            } else if (byte === ESC) {
              state = "osc52-escape";
            } else if (!discardOsc52) {
              if (osc52Data.length >= maxDataBytes) {
                osc52Data = [];
                discardOsc52 = true;
              } else {
                osc52Data.push(byte);
              }
            }
            break;

          case "osc52-escape":
            if (byte === BACKSLASH) {
              finishOsc52();
            } else {
              osc52Data = [];
              discardOsc52 = true;
              state = byte === ESC ? "osc52-escape" : "osc52";
            }
            break;
        }
      }

      return Uint8Array.from(output);
    },
    reset() {
      state = "normal";
      candidate = [];
      command = [];
      osc52Data = [];
      discardOsc52 = false;
    },
  };
}
