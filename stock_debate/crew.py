"""
StockDebateCrew — CrewAI @CrewBase implementation.

4 agents debate stock direction in 3 phases:
  Phase 1: Independent initial analyses (parallel)
  Phase 2: Cross-analyst rebuttals
  Phase 3: Portfolio Manager synthesis → final verdict
"""

import os

from crewai import Agent, Crew, LLM, Process, Task
from crewai.project import CrewBase, agent, crew, task


@CrewBase
class StockDebateCrew:
    agents_config = "config/agents.yaml"
    tasks_config = "config/tasks.yaml"

    def _llm(self) -> LLM:
        api_key = os.environ.get("ANTHROPIC_API_KEY")
        if not api_key:
            raise EnvironmentError("ANTHROPIC_API_KEY not set.")
        return LLM(
            model="anthropic/claude-opus-4-6",
            api_key=api_key,
            max_tokens=2000,
        )

    # ── Agents ──────────────────────────────────────

    @agent
    def technical_analyst(self) -> Agent:
        return Agent(
            config=self.agents_config["technical_analyst"],
            llm=self._llm(),
            verbose=True,
        )

    @agent
    def fundamental_analyst(self) -> Agent:
        return Agent(
            config=self.agents_config["fundamental_analyst"],
            llm=self._llm(),
            verbose=True,
        )

    @agent
    def macro_analyst(self) -> Agent:
        return Agent(
            config=self.agents_config["macro_analyst"],
            llm=self._llm(),
            verbose=True,
        )

    @agent
    def portfolio_manager(self) -> Agent:
        return Agent(
            config=self.agents_config["portfolio_manager"],
            llm=self._llm(),
            verbose=True,
        )

    # ── Phase 1: Initial analyses ────────────────────

    @task
    def technical_initial(self) -> Task:
        return Task(config=self.tasks_config["technical_initial"])

    @task
    def fundamental_initial(self) -> Task:
        return Task(config=self.tasks_config["fundamental_initial"])

    @task
    def macro_initial(self) -> Task:
        return Task(config=self.tasks_config["macro_initial"])

    # ── Phase 2: Rebuttals ───────────────────────────

    @task
    def technical_rebuttal(self) -> Task:
        return Task(config=self.tasks_config["technical_rebuttal"])

    @task
    def fundamental_rebuttal(self) -> Task:
        return Task(config=self.tasks_config["fundamental_rebuttal"])

    @task
    def macro_rebuttal(self) -> Task:
        return Task(config=self.tasks_config["macro_rebuttal"])

    # ── Phase 3: Final synthesis ─────────────────────

    @task
    def synthesis(self) -> Task:
        return Task(config=self.tasks_config["synthesis"])

    # ── Crew ─────────────────────────────────────────

    @crew
    def crew(self) -> Crew:
        return Crew(
            agents=self.agents,
            tasks=self.tasks,
            process=Process.sequential,
            verbose=True,
        )
