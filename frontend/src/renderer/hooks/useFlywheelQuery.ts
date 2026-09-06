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
	});
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
