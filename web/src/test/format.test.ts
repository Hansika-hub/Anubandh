import { compactRupees, countdown, label, newDealId, rupees, toPaise } from "../lib/format";

describe("money", () => {
  it("formats paise as rupees with Indian grouping", () => {
    expect(rupees(100_000_000)).toBe("₹10,00,000");
    expect(rupees(12_345)).toBe("₹123.45");
    expect(rupees(null)).toBe("—");
  });
  it("compacts to lakh and crore", () => {
    expect(compactRupees(80_000_000)).toBe("₹8 L");
    expect(compactRupees(600_000_000)).toBe("₹60 L");
    expect(compactRupees(1_250_000_000)).toBe("₹1.25 Cr");
    expect(compactRupees(800_000)).toBe("₹8,000");
  });
  it("parses rupee input to integer paise", () => {
    expect(toPaise("10,000")).toBe(1_000_000);
    expect(toPaise("₹ 12.5")).toBe(1250);
    expect(toPaise("1.234")).toBeNull();
    expect(toPaise("abc")).toBeNull();
  });
});

describe("time and labels", () => {
  it("counts down and flags overdue", () => {
    expect(countdown(3 * 86400 + 4 * 3600)).toBe("3d 4h");
    expect(countdown(5 * 3600 + 12 * 60)).toBe("5h 12m");
    expect(countdown(-2 * 86400)).toBe("overdue 2d 0h");
  });
  it("labels enum values", () => {
    expect(label("PARTIALLY_ACCEPTED")).toBe("Partially accepted");
  });
  it("makes deal ids the gateway accepts", () => {
    const id = newDealId(new Date(2026, 9, 20), () => 0.5);
    expect(id).toBe("DL-20261020-5500");
    expect(newDealId()).toMatch(/^DL-\d{8}-\d{4}$/);
  });
});
