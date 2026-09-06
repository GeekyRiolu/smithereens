import { createFileRoute } from "@tanstack/react-router";

import { LearningDashboard } from "../components/LearningDashboard";

export const Route = createFileRoute("/_shell/projects/$projectId_/learning")({
	component: LearningRoute,
});

function LearningRoute() {
	const { projectId } = Route.useParams();
	return <LearningDashboard projectId={projectId} />;
}
