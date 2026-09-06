import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Brain, FlaskConical, Play, Sparkles, TrendingDown, TrendingUp, Users } from "lucide-react";
import { useMemo } from "react";

import {
	type FlywheelCyclePoint,
	type FlywheelEpisode,
	type FlywheelMemoryEntry,
	type FlywheelOverview,
	flywheelOverviewQueryKey,
	flywheelOverviewQueryOptions,
	runFlywheelDemo,
	runFlywheelLiveDemo,
	runFlywheelSessionDemo,
} from "../hooks/useFlywheelQuery";
import { cn } from "../lib/utils";
import { Badge } from "./ui/badge";
import { Button } from "./ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "./ui/card";
import { Skeleton } from "./ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "./ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "./ui/tabs";

function pct(n: number): string {
	return `${Math.round(n * 100)}%`;
}

function tokens(n: number): string {
	if (n >= 1000) return `${(n / 1000).toFixed(1)}k`;
	return `${Math.round(n)}`;
}

type EpisodeOutcome = {
	success?: boolean;
	tokens?: number;
	agent?: string;
	team?: string;
	priority?: number;
	toolCalls?: number;
	costUsd?: number;
};

function parseOutcome(ep: FlywheelEpisode): EpisodeOutcome {
	try {
		return JSON.parse(ep.outcomeJson) as EpisodeOutcome;
	} catch {
		return {};
	}
}

// MiniLineChart renders one or more series over a shared 0..max range as inline
// SVG (no chart dependency). Values are plotted left→right across cycles.
function MiniLineChart({
	series,
	max,
	height = 120,
}: {
	series: { values: number[]; className: string; dashed?: boolean }[];
	max: number;
	height?: number;
}) {
	const width = 320;
	const pad = 6;
	const n = Math.max(...series.map((s) => s.values.length), 1);
	const x = (i: number) => (n <= 1 ? width / 2 : pad + (i * (width - 2 * pad)) / (n - 1));
	const y = (v: number) => height - pad - (max <= 0 ? 0 : (v / max) * (height - 2 * pad));
	return (
		<svg viewBox={`0 0 ${width} ${height}`} className="h-32 w-full" preserveAspectRatio="none" role="img">
			<line x1={0} y1={height - pad} x2={width} y2={height - pad} className="stroke-border" strokeWidth={1} />
			{series.map((s, si) => {
				if (s.values.length === 0) return null;
				const pts = s.values.map((v, i) => `${x(i)},${y(v)}`).join(" ");
				return (
					<g key={si} className={s.className}>
						<polyline
							points={pts}
							fill="none"
							stroke="currentColor"
							strokeWidth={2}
							strokeDasharray={s.dashed ? "4 3" : undefined}
							strokeLinejoin="round"
							strokeLinecap="round"
						/>
						{s.values.map((v, i) => (
							<circle key={i} cx={x(i)} cy={y(v)} r={2.5} fill="currentColor" />
						))}
					</g>
				);
			})}
		</svg>
	);
}

function StatTile({
	icon,
	label,
	value,
	hint,
}: {
	icon: React.ReactNode;
	label: string;
	value: string;
	hint?: string;
}) {
	return (
		<Card>
			<CardContent className="flex flex-col gap-1 p-4">
				<div className="flex items-center gap-2 text-muted-foreground text-xs">
					{icon}
					<span>{label}</span>
				</div>
				<div className="font-semibold text-2xl tabular-nums">{value}</div>
				{hint ? <div className="text-muted-foreground text-xs">{hint}</div> : null}
			</CardContent>
		</Card>
	);
}

function memoryStatusVariant(status: string): "success" | "accent" | "error" | "outline" | "neutral" {
	switch (status) {
		case "active":
			return "success";
		case "candidate":
			return "accent";
		case "quarantined":
			return "error";
		case "deprecated":
			return "outline";
		default:
			return "neutral";
	}
}

export function LearningDashboard({ projectId }: { projectId: string }) {
	const queryClient = useQueryClient();
	const query = useQuery(flywheelOverviewQueryOptions(projectId));

	const demo = useMutation({
		mutationFn: () => runFlywheelDemo(projectId),
		onSuccess: (data: FlywheelOverview) => {
			queryClient.setQueryData(flywheelOverviewQueryKey(projectId), data);
		},
	});
	const liveDemo = useMutation({
		mutationFn: () => runFlywheelLiveDemo(projectId),
		onSuccess: (data: FlywheelOverview) => {
			queryClient.setQueryData(flywheelOverviewQueryKey(projectId), data);
		},
	});
	const sessionDemo = useMutation({
		mutationFn: () => runFlywheelSessionDemo(projectId),
		onSuccess: (data: FlywheelOverview) => {
			queryClient.setQueryData(flywheelOverviewQueryKey(projectId), data);
		},
	});

	const overview = query.data;
	const cycles = overview?.cycles ?? [];
	const learningCycles = cycles.filter((c) => c.episodesSeen > 0);
	const last = cycles.length > 0 ? cycles[cycles.length - 1] : undefined;
	const first = cycles.length > 0 ? cycles[0] : undefined;

	const accuracyDelta = useMemo(() => {
		if (!first || !last) return 0;
		return last.metrics.accuracy - first.metrics.accuracy;
	}, [first, last]);

	if (query.isLoading) {
		return (
			<div className="flex flex-col gap-4 p-6">
				<Skeleton className="h-8 w-64" />
				<div className="grid grid-cols-2 gap-4 md:grid-cols-4">
					{[0, 1, 2, 3].map((i) => (
						<Skeleton key={i} className="h-24" />
					))}
				</div>
				<Skeleton className="h-64" />
			</div>
		);
	}

	const liveAvailable = overview?.liveAvailable ?? false;
	const liveRunning = overview?.liveRunning ?? false;
	const sessionAvailable = overview?.sessionAvailable ?? false;

	const runButton = (
		<Button onClick={() => demo.mutate()} disabled={demo.isPending || liveRunning}>
			<Play className="size-4" />
			{demo.isPending ? "Running…" : cycles.length > 0 ? "Run another cycle" : "Run learning demo"}
		</Button>
	);
	const liveButton = (
		<Button
			variant="secondary"
			onClick={() => liveDemo.mutate()}
			disabled={!liveAvailable || liveRunning || liveDemo.isPending}
			title={
				liveAvailable
					? "Run the real Claude Code agent on a few tasks (uses your Claude auth)"
					: "Requires the claude CLI (Claude Code) installed and authenticated"
			}
		>
			<Sparkles className="size-4" />
			{liveRunning ? "Live run in progress…" : "Run LIVE cycle (real Claude)"}
		</Button>
	);
	const sessionButton = (
		<Button
			variant="outline"
			onClick={() => sessionDemo.mutate()}
			disabled={!sessionAvailable || liveRunning || sessionDemo.isPending}
			title={
				sessionAvailable
					? "Run the cycle as real AO worker sessions — they appear on the Kanban and feed the learning"
					: "The worker-session executor is not wired on this daemon"
			}
		>
			<Users className="size-4" />
			{liveRunning ? "Running…" : "Run as AO worker sessions"}
		</Button>
	);

	return (
		<div className="flex h-full flex-col gap-6 overflow-y-auto p-6">
			<header className="flex flex-wrap items-center justify-between gap-3">
				<div className="flex items-center gap-3">
					<div className="flex size-9 items-center justify-center rounded-lg bg-card text-primary">
						<Brain className="size-5" />
					</div>
					<div>
						<h1 className="font-semibold text-xl">Flywheel — Learning</h1>
						<p className="text-muted-foreground text-sm">
							The agent gets measurably better every cycle. Memory is only promoted when it passes the eval gate.
						</p>
					</div>
				</div>
				<div className="flex flex-wrap items-center gap-2">
					{runButton}
					{liveButton}
					{sessionButton}
				</div>
			</header>

			{query.isError ? (
				<Card>
					<CardContent className="p-4 text-destructive text-sm">
						{(query.error as Error)?.message ?? "Failed to load the Flywheel dashboard."}
					</CardContent>
				</Card>
			) : null}

			{liveRunning ? (
				<Card>
					<CardContent className="flex items-center gap-3 p-4 text-sm">
						<span className="size-2 animate-pulse rounded-full bg-success" />
						<span>
							Live run in progress — {overview?.liveStep || "working"}… records update automatically.
						</span>
					</CardContent>
				</Card>
			) : null}
			{overview?.liveError ? (
				<Card>
					<CardContent className="p-4 text-destructive text-sm">Live run error: {overview.liveError}</CardContent>
				</Card>
			) : null}

			{cycles.length === 0 ? (
				<Card>
					<CardContent className="flex flex-col items-center gap-3 p-10 text-center">
						<Sparkles className="size-8 text-muted-foreground" />
						<div className="font-medium">No learning cycles yet</div>
						<p className="max-w-md text-muted-foreground text-sm">
							Run the demo to execute a cold baseline plus consolidation cycles across agents. You&apos;ll see accuracy
							rise, cost fall, memory grow, and the eval gate promote or quarantine each lesson.
						</p>
						{runButton}
					</CardContent>
				</Card>
			) : (
				<>
					<section className="grid grid-cols-2 gap-4 md:grid-cols-4">
						<StatTile
							icon={<TrendingUp className="size-3.5" />}
							label="Task accuracy"
							value={last ? pct(last.metrics.accuracy) : "—"}
							hint={accuracyDelta > 0 ? `+${pct(accuracyDelta)} since cold` : "baseline"}
						/>
						<StatTile
							icon={<TrendingDown className="size-3.5" />}
							label="Avg tokens / run"
							value={last ? tokens(last.metrics.avgTokens) : "—"}
							hint={first && last ? `from ${tokens(first.metrics.avgTokens)}` : undefined}
						/>
						<StatTile
							icon={<Brain className="size-3.5" />}
							label="Active lessons"
							value={`${overview?.memory.active ?? 0}`}
							hint={`${overview?.memory.quarantined ?? 0} quarantined`}
						/>
						<StatTile
							icon={<FlaskConical className="size-3.5" />}
							label="Ablation gap"
							value={last ? pct(last.metrics.accuracy - last.ablationAccuracy) : "—"}
							hint="memory on − off"
						/>
					</section>

					<div className="grid gap-4 lg:grid-cols-2">
						<Card>
							<CardHeader>
								<CardTitle className="text-sm">Quality per cycle</CardTitle>
							</CardHeader>
							<CardContent>
								<MiniLineChart
									max={1}
									series={[
										{ values: cycles.map((c) => c.metrics.accuracy), className: "text-success" },
										{ values: cycles.map((c) => c.ablationAccuracy), className: "text-muted-foreground", dashed: true },
									]}
								/>
								<div className="mt-2 flex gap-4 text-muted-foreground text-xs">
									<span className="flex items-center gap-1">
										<span className="inline-block h-0.5 w-4 bg-success" /> memory on
									</span>
									<span className="flex items-center gap-1">
										<span className="inline-block h-0.5 w-4 border-muted-foreground border-t border-dashed" /> memory off
										(ablation)
									</span>
								</div>
							</CardContent>
						</Card>
						<Card>
							<CardHeader>
								<CardTitle className="text-sm">Cost per cycle (avg tokens)</CardTitle>
							</CardHeader>
							<CardContent>
								<MiniLineChart
									max={Math.max(...cycles.map((c) => c.metrics.avgTokens), 1)}
									series={[{ values: cycles.map((c) => c.metrics.avgTokens), className: "text-primary" }]}
								/>
								<div className="mt-2 text-muted-foreground text-xs">
									Lower is better — mastered work is routed to the cheap path.
								</div>
							</CardContent>
						</Card>
					</div>

					<Tabs defaultValue="memory">
						<TabsList>
							<TabsTrigger value="memory">Memory ({overview?.memory.entries.length ?? 0})</TabsTrigger>
							<TabsTrigger value="cycles">Cycles ({learningCycles.length})</TabsTrigger>
							<TabsTrigger value="episodes">Episodes ({overview?.recentEpisodes.length ?? 0})</TabsTrigger>
						</TabsList>

						<TabsContent value="memory">
							<MemoryTable entries={overview?.memory.entries ?? []} />
						</TabsContent>
						<TabsContent value="cycles">
							<CyclesTable cycles={cycles} />
						</TabsContent>
						<TabsContent value="episodes">
							<EpisodesTable episodes={overview?.recentEpisodes ?? []} />
						</TabsContent>
					</Tabs>
				</>
			)}
		</div>
	);
}

function MemoryTable({ entries }: { entries: FlywheelMemoryEntry[] }) {
	if (entries.length === 0) {
		return <p className="p-4 text-muted-foreground text-sm">No memory yet.</p>;
	}
	return (
		<Table>
			<TableHeader>
				<TableRow>
					<TableHead>Lesson</TableHead>
					<TableHead>Kind</TableHead>
					<TableHead>Status</TableHead>
					<TableHead className="text-right">Confidence</TableHead>
					<TableHead className="text-right">Support</TableHead>
				</TableRow>
			</TableHeader>
			<TableBody>
				{entries.map((m) => (
					<TableRow key={m.id}>
						<TableCell className="max-w-md">
							<div className="font-medium">{m.title}</div>
							{m.body ? <div className="truncate text-muted-foreground text-xs">{m.body}</div> : null}
						</TableCell>
						<TableCell>
							<Badge variant="neutral">{m.kind.replace("_", " ")}</Badge>
						</TableCell>
						<TableCell>
							<Badge variant={memoryStatusVariant(m.status)}>{m.status}</Badge>
						</TableCell>
						<TableCell className="text-right tabular-nums">{m.confidence.toFixed(2)}</TableCell>
						<TableCell className="text-right tabular-nums">{m.supportCount}</TableCell>
					</TableRow>
				))}
			</TableBody>
		</Table>
	);
}

function CyclesTable({ cycles }: { cycles: FlywheelCyclePoint[] }) {
	return (
		<Table>
			<TableHeader>
				<TableRow>
					<TableHead>#</TableHead>
					<TableHead className="text-right">Accuracy</TableHead>
					<TableHead className="text-right">Avg tokens</TableHead>
					<TableHead className="text-right">Tool err</TableHead>
					<TableHead className="text-right">Promoted</TableHead>
					<TableHead className="text-right">Quarantined</TableHead>
					<TableHead className="text-right">Ablation</TableHead>
				</TableRow>
			</TableHeader>
			<TableBody>
				{cycles.map((c, i) => (
					<TableRow key={c.id}>
						<TableCell>{i === 0 ? "cold" : i}</TableCell>
						<TableCell className="text-right tabular-nums">{pct(c.metrics.accuracy)}</TableCell>
						<TableCell className="text-right tabular-nums">{tokens(c.metrics.avgTokens)}</TableCell>
						<TableCell className="text-right tabular-nums">{c.metrics.toolErrorRate.toFixed(2)}</TableCell>
						<TableCell className="text-right tabular-nums text-success">{c.promoted || ""}</TableCell>
						<TableCell className="text-right tabular-nums text-destructive">{c.quarantined || ""}</TableCell>
						<TableCell className="text-right tabular-nums">{pct(c.metrics.accuracy - c.ablationAccuracy)}</TableCell>
					</TableRow>
				))}
			</TableBody>
		</Table>
	);
}

function EpisodesTable({ episodes }: { episodes: FlywheelEpisode[] }) {
	if (episodes.length === 0) {
		return <p className="p-4 text-muted-foreground text-sm">No episodes yet.</p>;
	}
	return (
		<Table>
			<TableHeader>
				<TableRow>
					<TableHead>Task</TableHead>
					<TableHead>Agent</TableHead>
					<TableHead>Outcome</TableHead>
					<TableHead className="text-right">Tools</TableHead>
					<TableHead className="text-right">Tokens</TableHead>
					<TableHead className="text-right">Cost</TableHead>
				</TableRow>
			</TableHeader>
			<TableBody>
				{episodes.map((ep) => {
					const o = parseOutcome(ep);
					return (
						<TableRow key={ep.id}>
							<TableCell>{ep.taskType}</TableCell>
							<TableCell>
								<Badge variant="outline">{o.agent ?? ep.sessionId ?? "—"}</Badge>
							</TableCell>
							<TableCell>
								<span className={cn("text-sm", o.success ? "text-success" : "text-destructive")}>
									{o.success ? "correct" : "wrong"}
									{o.team ? ` · ${o.team}/P${o.priority}` : ""}
								</span>
							</TableCell>
							<TableCell className="text-right tabular-nums">{o.toolCalls ?? "—"}</TableCell>
							<TableCell className="text-right tabular-nums">{o.tokens ? tokens(o.tokens) : "—"}</TableCell>
							<TableCell className="text-right tabular-nums">{o.costUsd ? `$${o.costUsd.toFixed(3)}` : "—"}</TableCell>
						</TableRow>
					);
				})}
			</TableBody>
		</Table>
	);
}
