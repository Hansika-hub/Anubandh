import { LineChart } from "echarts/charts";
import { GridComponent, LegendComponent, MarkLineComponent, TooltipComponent } from "echarts/components";
import * as echarts from "echarts/core";
import { CanvasRenderer } from "echarts/renderers";
import { useEffect, useRef } from "react";
import type { Band, BandPoint } from "../api/types";
import { compactRupees } from "../lib/format";

echarts.use([LineChart, GridComponent, TooltipComponent, LegendComponent, MarkLineComponent, CanvasRenderer]);

/** The forecast returns p10/p50/p90; BUILD.md §10.3 names them p10Paise/...; accept both. */
export function quantiles(p: BandPoint): [number, number, number] {
  return [p.p10Paise ?? p.p10 ?? 0, p.p50Paise ?? p.p50 ?? 0, p.p90Paise ?? p.p90 ?? 0];
}

function cssVar(name: string, fallback: string): string {
  const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  return v || fallback;
}

export function BandChart({ band }: { band: Band }) {
  const el = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!el.current) return;
    const chart = echarts.init(el.current, undefined, { renderer: "canvas" });
    const intraday = band.horizon === "intraday";
    const xs = band.points.map((p) =>
      new Date(p.ts).toLocaleString("en-IN", intraday ? { hour: "2-digit", minute: "2-digit", hour12: false } : { day: "2-digit", month: "short" }),
    );
    const q = band.points.map(quantiles);
    const accent = cssVar("--accent", "#0f5c4d");
    const band10 = cssVar("--band", "rgba(15,92,77,0.18)");
    const danger = cssVar("--bad", "#b42318");
    const ink = cssVar("--muted", "#667");
    chart.setOption({
      animation: false,
      grid: { left: 70, right: 20, top: 30, bottom: 30 },
      tooltip: {
        trigger: "axis",
        valueFormatter: (v: number) => compactRupees(v),
        formatter: (params: { dataIndex: number }[]) => {
          const i = params[0]?.dataIndex ?? 0;
          const [p10, p50, p90] = q[i] ?? [0, 0, 0];
          return `${xs[i]}<br/>P90 ${compactRupees(p90)}<br/><b>P50 ${compactRupees(p50)}</b><br/>P10 ${compactRupees(p10)}`;
        },
      },
      legend: { data: ["P50 (median)", "P10-P90 band"], top: 0, textStyle: { color: ink } },
      xAxis: { type: "category", data: xs, boundaryGap: false, axisLabel: { color: ink } },
      yAxis: { type: "value", axisLabel: { color: ink, formatter: (v: number) => compactRupees(v) }, splitLine: { lineStyle: { opacity: 0.25 } } },
      series: [
        // Stacked trick for a filled band: invisible P10 base + (P90 - P10) area on top.
        { name: "base", type: "line", data: q.map((x) => x[0]), stack: "band", lineStyle: { opacity: 0 }, symbol: "none", silent: true },
        {
          name: "P10-P90 band",
          type: "line",
          data: q.map((x) => x[2] - x[0]),
          stack: "band",
          lineStyle: { opacity: 0 },
          areaStyle: { color: band10 },
          symbol: "none",
          silent: true,
        },
        {
          name: "P50 (median)",
          type: "line",
          data: q.map((x) => x[1]),
          symbol: "none",
          lineStyle: { width: 2.5, color: accent },
          itemStyle: { color: accent },
          markLine: {
            symbol: "none",
            silent: true,
            label: { formatter: `minimum buffer ${compactRupees(band.minBufferPaise)}`, color: danger, position: "insideEndTop" },
            lineStyle: { color: danger, type: "dashed" },
            data: [{ yAxis: band.minBufferPaise }],
          },
        },
      ],
    });
    const ro = new ResizeObserver(() => chart.resize());
    ro.observe(el.current);
    return () => {
      ro.disconnect();
      chart.dispose();
    };
  }, [band]);
  return <div ref={el} className="chart" role="img" aria-label={`cash position forecast, ${band.horizon}`} />;
}
