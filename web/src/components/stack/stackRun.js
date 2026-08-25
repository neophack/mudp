// Module-level state for a stack deploy/down run: the streamed lines are kept
// so reopening the terminal replays the whole session, and the active/verb
// fields let the Stacks view ignore stale output after a switch.
import { reactive } from "vue";

export const stackRun = reactive({
  active: false,
  stackId: 0,
  verb: "", // "up" | "down"
  lines: [],
  error: "",
  done: false,
});
