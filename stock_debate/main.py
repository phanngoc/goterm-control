"""
Stock Debate Prediction Tool — CrewAI Edition
4 CrewAI agents debate to predict stock price direction using vnstock data.

Agents:
  1. Technical Analyst     - price patterns, indicators (MA, RSI, MACD)
  2. Fundamental Analyst   - financial health, valuation
  3. Macro/Sentiment Analyst - market context, sector trend
  4. Portfolio Manager     - synthesizes the debate into a final verdict

Usage:
  python main.py --symbol VNM
  python main.py --symbol HPG --days 60
"""

import argparse
import sys

try:
    from dotenv import load_dotenv
    load_dotenv()
except ImportError:
    pass

from crew import StockDebateCrew
from data import build_context, fetch_stock_data


def run_debate(symbol: str, days: int = 90) -> str:
    print(f"\n{'='*60}")
    print(f"  STOCK DEBATE PREDICTION: {symbol.upper()}  (CrewAI)")
    print(f"{'='*60}\n")

    print("Fetching stock data from vnstock...")
    try:
        data = fetch_stock_data(symbol, days)
    except Exception as e:
        print(f"Failed to fetch data: {e}")
        sys.exit(1)

    context = build_context(data)
    print(f"Data loaded. {len(data['history'])} sessions.\n")

    result = StockDebateCrew().crew().kickoff(
        inputs={"symbol": symbol, "context": context}
    )

    print("\n" + "=" * 60)
    print("  FINAL VERDICT")
    print("=" * 60)
    print(result.raw)
    print("=" * 60 + "\n")

    return result.raw


def main():
    parser = argparse.ArgumentParser(
        description="Stock debate prediction using CrewAI + Claude + vnstock"
    )
    parser.add_argument("--symbol", required=True, help="Stock ticker (e.g. VNM, HPG, FPT)")
    parser.add_argument("--days", type=int, default=90, help="Historical days to fetch (default: 90)")
    args = parser.parse_args()

    run_debate(symbol=args.symbol.upper(), days=args.days)


if __name__ == "__main__":
    main()
