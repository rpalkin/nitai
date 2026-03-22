import { createConnectTransport } from "@connectrpc/connect-web";
import { createClient } from "@connectrpc/connect";
import { AuthService } from "@gen/api/v1/auth_pb";
import { ProviderService } from "@gen/api/v1/provider_pb";
import { RepoService } from "@gen/api/v1/repo_pb";
import { ReviewService } from "@gen/api/v1/review_pb";
import { InstructionService } from "@gen/api/v1/instruction_pb";
import { ActivityService } from "@gen/api/v1/activity_pb";

const transport = createConnectTransport({
  baseUrl: "/",
});

export const authClient = createClient(AuthService, transport);
export const providerClient = createClient(ProviderService, transport);
export const repoClient = createClient(RepoService, transport);
export const reviewClient = createClient(ReviewService, transport);
export const instructionClient = createClient(InstructionService, transport);
export const activityClient = createClient(ActivityService, transport);

export { transport };