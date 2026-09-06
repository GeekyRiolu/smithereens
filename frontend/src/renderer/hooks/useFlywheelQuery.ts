import { queryOptions } from "@tanstack/react-query";

import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage } from "../lib/api-client";

export type FlywheelOverview = components["schemas"]["FlywheelOverview"];
export type FlywheelCyclePoint = components["schemas"]["FlywheelCyclePoint"];
export type FlywheelMemoryEntry = components["schemas"]["FlywheelMemoryEntry"];
export type FlywheelEpisode = components["schemas"]["FlywheelEpisode"];

export const flywheelOverviewQueryKey = (projectId: string) =>
	["flywheel-overview", projectId] as const;

export function flywheelOverviewQueryOptions(projectId: string) {
	return queryOptions({
		queryKey: flywheelOverviewQueryKey(projectId),
		queryFn: async (): Promise<FlywheelOverview> => {
			const result = await apiClient.GET("/api/v1/projects/{projectId}/flywheel/overview", {
				params: { path: { projectId } },
			});
			if (result.error || !result.data) {
				throw new Error(apiErrorMessage(result.error, "Could not load the Flywheel dashboard."));
			}
			return result.data as FlywheelOverview;
		},
		enabled: projectId !== "",
		// While a live agent run is in flight, poll so new episodes/cycles appear.
		refetchInterval: (query) => (query.state.data?.liveRunning ? 2500 : false),
	});
}

// runFlywheelLiveDemo starts a live learning run with the REAL Claude Code agent
// (async on the daemon); the returned overview has liveRunning=true and the UI
// polls until it completes.
export async function runFlywheelLiveDemo(projectId: string): Promise<FlywheelOverview> {
	const result = await apiClient.POST("/api/v1/projects/{projectId}/flywheel/live-demo", {
		params: { path: { projectId } },
	});
	if (result.error || !result.data) {
		throw new Error(apiErrorMessage(result.error, "Could not start the live Flywheel run."));
	}
	return result.data as FlywheelOverview;
}

// runFlywheelSessionDemo starts a learning run executed as REAL AO worker
// sessions (Option 2 — visible on the Kanban); async, polled like the live run.
export async function runFlywheelSessionDemo(projectId: string): Promise<FlywheelOverview> {
	const result = await apiClient.POST("/api/v1/projects/{projectId}/flywheel/session-demo", {
		params: { path: { projectId } },
	});
	if (result.error || !result.data) {
		throw new Error(apiErrorMessage(result.error, "Could not start the worker-session run."));
	}
	return result.data as FlywheelOverview;
}

// runFlywheelDemo runs a full learning demo (cold baseline + consolidation cycles
// across agents) for the project and returns the updated dashboard.
export async function runFlywheelDemo(projectId: string): Promise<FlywheelOverview> {
	const result = await apiClient.POST("/api/v1/projects/{projectId}/flywheel/demo", {
		params: { path: { projectId } },
	});
	if (result.error || !result.data) {
		throw new Error(apiErrorMessage(result.error, "Could not run the Flywheel demo."));
	}
	return result.data as FlywheelOverview;
}
