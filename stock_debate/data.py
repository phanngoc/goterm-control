"""
Data fetching and technical indicator computation via vnstock.
"""

from datetime import datetime, timedelta

import pandas as pd


def fetch_stock_data(symbol: str, days: int = 90) -> dict:
    """Fetch price history and financial data from vnstock."""
    from vnstock import Vnstock

    end = datetime.today().strftime("%Y-%m-%d")
    start = (datetime.today() - timedelta(days=days)).strftime("%Y-%m-%d")

    s = Vnstock().stock(symbol=symbol, source="VCI")
    hist = s.quote.history(start=start, end=end, interval="1D")

    try:
        income = s.finance.income_statement(period="year", lang="en").head(4)
    except Exception:
        income = pd.DataFrame()

    try:
        balance = s.finance.balance_sheet(period="year", lang="en").head(4)
    except Exception:
        balance = pd.DataFrame()

    try:
        ratio = s.finance.ratio(period="year", lang="en").head(4)
    except Exception:
        ratio = pd.DataFrame()

    return {
        "symbol": symbol,
        "start": start,
        "end": end,
        "history": hist,
        "income": income,
        "balance": balance,
        "ratio": ratio,
    }


def compute_indicators(hist: pd.DataFrame) -> dict:
    """Compute basic technical indicators from OHLCV data."""
    close = hist["close"]

    ma20 = close.rolling(20).mean().iloc[-1]
    ma50 = close.rolling(50).mean().iloc[-1] if len(close) >= 50 else None
    current = close.iloc[-1]
    prev_close = close.iloc[-2]

    # RSI (14)
    delta = close.diff()
    gain = delta.clip(lower=0).rolling(14).mean()
    loss = (-delta.clip(upper=0)).rolling(14).mean()
    rs = gain / loss
    rsi = (100 - 100 / (1 + rs)).iloc[-1]

    # MACD (12, 26, 9)
    ema12 = close.ewm(span=12).mean()
    ema26 = close.ewm(span=26).mean()
    macd_line = ema12 - ema26
    signal_line = macd_line.ewm(span=9).mean()
    macd_hist = macd_line - signal_line

    # Volume trend (last 5 days vs 20-day avg)
    vol_avg20 = hist["volume"].rolling(20).mean().iloc[-1]
    vol_last5 = hist["volume"].tail(5).mean()

    high_period = hist["high"].max()
    low_period = hist["low"].min()

    return {
        "current_price": round(current, 2),
        "prev_close": round(prev_close, 2),
        "change_pct": round((current - prev_close) / prev_close * 100, 2),
        "ma20": round(ma20, 2) if not pd.isna(ma20) else None,
        "ma50": round(ma50, 2) if ma50 is not None and not pd.isna(ma50) else None,
        "rsi14": round(rsi, 2) if not pd.isna(rsi) else None,
        "macd_line": round(macd_line.iloc[-1], 4) if not pd.isna(macd_line.iloc[-1]) else None,
        "macd_histogram": round(macd_hist.iloc[-1], 4) if not pd.isna(macd_hist.iloc[-1]) else None,
        "volume_vs_avg": round(vol_last5 / vol_avg20, 2) if vol_avg20 > 0 else None,
        "period_high": round(high_period, 2),
        "period_low": round(low_period, 2),
        "price_vs_high_pct": round((current - high_period) / high_period * 100, 2),
    }


def build_context(data: dict) -> str:
    """Format stock data and indicators into a text block for agent prompts."""
    symbol = data["symbol"]
    hist = data["history"]
    ind = compute_indicators(hist)

    lines = [
        f"=== STOCK: {symbol} | Period: {data['start']} → {data['end']} ===",
        "",
        "── PRICE & TECHNICAL ──",
        f"Current price    : {ind['current_price']:,}",
        f"Prev close       : {ind['prev_close']:,}  ({ind['change_pct']:+.2f}%)",
        f"MA20             : {ind['ma20']:,}" if ind["ma20"] else "MA20: N/A",
        f"MA50             : {ind['ma50']:,}" if ind["ma50"] else "MA50: N/A",
        f"RSI(14)          : {ind['rsi14']}" if ind["rsi14"] else "RSI: N/A",
        f"MACD line        : {ind['macd_line']}" if ind["macd_line"] else "MACD: N/A",
        f"MACD histogram   : {ind['macd_histogram']}" if ind["macd_histogram"] else "",
        f"Volume vs 20d avg: {ind['volume_vs_avg']}x" if ind["volume_vs_avg"] else "",
        f"Period high      : {ind['period_high']:,}  ({ind['price_vs_high_pct']:+.1f}% from high)",
        f"Period low       : {ind['period_low']:,}",
        "",
        "── RECENT PRICE (last 10 sessions) ──",
    ]

    recent = hist.tail(10)[["time", "open", "high", "low", "close", "volume"]].copy()
    recent["volume"] = recent["volume"].apply(lambda v: f"{v:,.0f}")
    lines.append(recent.to_string(index=False))

    if not data["income"].empty:
        lines += ["", "── INCOME STATEMENT (annual) ──", data["income"].to_string(index=False)]

    if not data["ratio"].empty:
        lines += ["", "── KEY RATIOS ──", data["ratio"].to_string(index=False)]

    return "\n".join(lines)
