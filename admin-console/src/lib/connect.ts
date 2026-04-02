import { createConnectTransport } from "@connectrpc/connect-web";
import { createClient, type Interceptor } from "@connectrpc/connect";
import { AuthService } from "@gen/api/v1/auth_pb";
import { ProviderService } from "@gen/api/v1/provider_pb";
import { RepoService } from "@gen/api/v1/repo_pb";
import { ReviewService } from "@gen/api/v1/review_pb";
import { InstructionService } from "@gen/api/v1/instruction_pb";
import { ActivityService } from "@gen/api/v1/activity_pb";
import { TOKEN_KEY } from "./auth-constants";

const authInterceptor: Interceptor = (next) => async (req) => {
  const token = localStorage.getItem(TOKEN_KEY);
  if (token) {
    req.header.set("Authorization", `Bearer ${token}`);
  }
  return next(req);
};

export function createAuthTransport() {
  return createConnectTransport({
    baseUrl: "/",
    interceptors: [authInterceptor],
  });
}

const transport = createConnectTransport({
  baseUrl: "/",
  interceptors: [authInterceptor],
});

export const authClient = createClient(AuthService, transport);
export const providerClient = createClient(ProviderService, transport);
export const repoClient = createClient(RepoService, transport);
export const reviewClient = createClient(ReviewService, transport);
export const instructionClient = createClient(InstructionService, transport);
export const activityClient = createClient(ActivityService, transport);

export { transport };