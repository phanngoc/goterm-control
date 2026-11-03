# Attention economy

> Take as little human attention as possible, and give as much value as possible.

The principle comes from DeepSeek's [dsh-superagent prototype](https://github.com/deepseek-ai/dsh-superagent-prototype) (docs/000-philosophy.md). Every message, question and notification an agent sends costs a person's attention, and has to be worth more than that. BomClaw already had much of this: a progress line that is deleted when the run ends, results posted back to the thread they came from, and a heartbeat that answers `NO_REPLY` to nobody. This document records what was adopted from the rest, and how to measure it.

## What was adopted

| dsh-superagent idea | Here |
|---|---|
| Ask **one concrete question, in a structured form**, so it reaches the person instead of staying inside a long report | `bomclaw task block --on human --ask "<question>" --note "<evidence>"`. The question has its own column (`tasks.question`, schema v13) and is what Telegram and the room show, exactly as written. Before this, the person saw the note's last paragraph, which was the evidence whenever an agent wrote the question first. An unblock records `Question: … / Answer from …` together in the checkpoint, so the next run knows what was decided. |
| **Speak only when someone must act or can use a result** | The reporter no longer announces a cancel: `task cancel` is a person stopping the work, and telling them so is noise. The report is still marked as delivered. |
| **Stop by impact, not effort**: choose reasonable defaults; stop only for a real decision | The worker prompt (`taskPrompt`) names what is genuinely the person's to decide: product or design direction, missing access, and actions that are irreversible or speak for them. Everything else is decided by the agent and noted in the result. |
| **Report honestly**: a draft is marked unfinished; "nothing to do" is a valid ending | The worker prompt asks the agent to lead with the outcome, say what it checked and what it did not, call a partial result partial, and finish a no-op in one sentence. |
| **Short messages** | The room prompt (`MentionWatcher.prompt`) asks the agent to lead with the answer, with no restated question, narration or offers of more help. |

Not adopted: dsh-superagent's separate orchestrator/worker split and its Eval/admission layer. BomClaw's rooms already route by `@mention` and a shared queue, and a second routing model would add concepts rather than remove attention cost.

## Measuring it: `bomclaw bench`

A suite is a set of small tasks, each with a shell check and an expected behaviour (`bench/attention-suite.json`):

- **autonomous**: routine work. It should finish without asking. Asking here is an *unnecessary interruption*.
- **noop**: nothing to do. It should change nothing and say so briefly. Changing files, or a long report, is noise.
- **decision**: only a person can decide, or the action is irreversible or outward-facing. It should ask one question and touch nothing. Going ahead is a *missed decision*: a guess made in the person's name.

```sh
bomclaw bench run --to bomclaw --label "baseline"          # writes ~/.goterm-shared/data/bench/<time>.json
bomclaw bench run --to bomclaw --label "after" --baseline <baseline.json>
bomclaw bench report <file.json> --baseline <other.json>
```

The bench uses the real queue, runner and prompts. Its tasks are created as `bench`, so nobody is paged when a decision case asks and no result is reported. It reads the question and then cancels the task.

The scorecard lists: **behaved** (right work and right amount of asking), checks passed, unnecessary interruptions, missed decisions, words a person reads (results plus questions), mean runs per case and median wall time. Each number is a count or a plain average of the per-case rows, so it can be recomputed by hand. There is no composite score.

The suite is tested too. `TestTheShippedSuiteChecksAreSound` runs each case's setup and its reference solution, and fails if a check accepts work that was never done, rejects the right answer, or misses a tampered file. It has already caught one broken check: a stale `__pycache__` made `fix-failing-test` grade old bytecode.

To compare agents, run the same suite with `--to bomclaw2` / `--to bomclaw3`. To compare a prompt change, run before and after with `--baseline`. Model output varies, so one run per side is a signal, not a verdict. Run each side two or three times before concluding.
