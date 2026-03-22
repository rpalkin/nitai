import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { authClient, providerClient, repoClient, reviewClient, instructionClient, activityClient, transport, createAuthTransport } from "../lib/connect";

describe("ConnectRPC clients", () => {
  it("exports a configured transport", () => {
    expect(transport).toBeDefined();
  });

  it("exports authClient with expected service methods", () => {
    expect(authClient).toBeDefined();
    expect(typeof authClient.getMe).toBe("function");
    expect(typeof authClient.login).toBe("function");
    expect(typeof authClient.register).toBe("function");
  });

  it("exports providerClient", () => {
    expect(providerClient).toBeDefined();
  });

  it("exports repoClient", () => {
    expect(repoClient).toBeDefined();
  });

  it("exports reviewClient", () => {
    expect(reviewClient).toBeDefined();
  });

  it("exports instructionClient", () => {
    expect(instructionClient).toBeDefined();
  });

  it("exports activityClient", () => {
    expect(activityClient).toBeDefined();
  });
});

describe("createAuthTransport", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    localStorage.clear();
  });

  it("exports createAuthTransport function", () => {
    expect(createAuthTransport).toBeDefined();
    expect(typeof createAuthTransport).toBe("function");
  });

  it("returns a transport", () => {
    localStorage.setItem("auth_token", "test-jwt-token");

    const authTransport = createAuthTransport();
    expect(authTransport).toBeDefined();
  });
});