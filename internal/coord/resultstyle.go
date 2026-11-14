package coord

// ResultStyle is how a result is written, for every task prompt that asks
// for one. Results land in the room and on the owner's phone, and they used
// to arrive as one English paragraph of every check that was run: hard to
// read, harder to act on. This asks for what Claude Code does at the end of
// a turn — outcome first, a few short lines, code formatting for the things
// you would copy — in the owner's language.
const ResultStyle = "Write the result in Vietnamese (keep ids, paths, commands, numbers and technical terms as they are), " +
	"laid out like Claude Code's final message:\n" +
	"  - line 1: the outcome in one short sentence;\n" +
	"  - then at most 6 short `- ` bullets: what was done or checked and the evidence (numbers, PR, commit);\n" +
	"  - `backticks` for ids, paths and commands;\n" +
	"  - a last line `**Còn lại:** …` only if something is left or needs the owner.\n" +
	"No paragraph longer than 3 lines, no log of every check, no repeating the title.\n"
