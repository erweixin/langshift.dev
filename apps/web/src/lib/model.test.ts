import { describe, expect, it } from "vitest";
import { demoCapabilities, evidenceLevelLabels, evidenceLevelOrder } from "./model";

describe("evidence language", () => {
  it("uses a finite ordered evidence scale with a stated basis", () => {
    expect(evidenceLevelOrder).toEqual(["inferred", "user_confirmed", "demonstrated", "applied", "reviewer_verified"]);
    expect(demoCapabilities.every((capability) => capability.basis.length > 10)).toBe(true);
  });

  it("does not turn capability judgments into false precision", () => {
    const renderedContract = JSON.stringify({ demoCapabilities, evidenceLevelLabels });
    expect(renderedContract).not.toMatch(/\b\d{1,3}%/);
    expect(renderedContract).not.toContain("confidence_score");
  });
});
