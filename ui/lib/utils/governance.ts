/**
 * Parses a duration string (e.g., "1m", "5m", "1h", "1d", "1w", "1M") into human readable format
 */
export function parseResetPeriod(duration: string): string {
	if (!duration) return "Unknown";

	const timeValue = parseInt(duration.slice(0, -1));
	const timeUnit = duration.slice(-1);

	const unitMap: Record<string, { singular: string; plural: string }> = {
		s: { singular: "second", plural: "seconds" },
		m: { singular: "minute", plural: "minutes" },
		h: { singular: "hour", plural: "hours" },
		d: { singular: "day", plural: "days" },
		w: { singular: "week", plural: "weeks" },
		M: { singular: "month", plural: "months" },
		y: { singular: "year", plural: "years" },
	};

	const unit = unitMap[timeUnit];
	if (!unit) return duration;

	const unitName = timeValue === 1 ? unit.singular : unit.plural;
	return `${timeValue} ${unitName}`;
}

import { formatCompactNumber } from "./numbers";

export function formatCurrency(dollars: number) {
	return `$${dollars.toFixed(2)}`;
}

export interface BudgetOverrideFields {
	max_limit: number;
	override_amount?: number;
	override_mode?: "cycles" | "forever";
	override_cycles_remaining?: number;
}

/** Returns whether a budget has a complete, currently active override. */
export function hasActiveBudgetOverride(budget: BudgetOverrideFields): boolean {
	if (!budget.override_amount || budget.override_amount <= 0) return false;
	return budget.override_mode === "forever" || (budget.override_mode === "cycles" && (budget.override_cycles_remaining ?? 0) > 0);
}

/** Returns the base limit plus the active additive override. */
export function getEffectiveBudgetLimit(budget: BudgetOverrideFields): number {
	return budget.max_limit + (hasActiveBudgetOverride(budget) ? (budget.override_amount ?? 0) : 0);
}

/** Validates the operator-entered override fields before sending them to the API. */
export function validateBudgetOverride(amount: number, mode: "cycles" | "forever", cycles: number): string | null {
	if (!Number.isFinite(amount) || amount <= 0) return "Additional budget must be greater than 0.";
	if (mode === "cycles" && (!Number.isInteger(cycles) || cycles <= 0)) return "Reset cycles must be a positive whole number.";
	return null;
}

/** Calculates when a cycle-based override will expire on the budget's current reset schedule. */
export function getBudgetOverrideValidUntil(
	budget: Pick<BudgetOverrideFields, "max_limit"> & { last_reset: string; reset_duration: string },
	cycles: number,
	calendarAligned = false,
): Date | null {
	if (!Number.isInteger(cycles) || cycles <= 0) return null;
	const match = /^(\d+)([smhdwMyY])$/.exec(budget.reset_duration);
	const validUntil = new Date(budget.last_reset);
	if (!match || Number.isNaN(validUntil.getTime())) return null;

	const durationValue = Number(match[1]) * cycles;
	switch (match[2]) {
		case "s":
			validUntil.setTime(validUntil.getTime() + durationValue * 1000);
			break;
		case "m":
			validUntil.setTime(validUntil.getTime() + durationValue * 60 * 1000);
			break;
		case "h":
			validUntil.setTime(validUntil.getTime() + durationValue * 60 * 60 * 1000);
			break;
		case "d":
			validUntil.setUTCDate(validUntil.getUTCDate() + durationValue);
			break;
		case "w":
			validUntil.setUTCDate(validUntil.getUTCDate() + durationValue * 7);
			break;
		case "M":
			if (calendarAligned) {
				validUntil.setUTCMonth(validUntil.getUTCMonth() + durationValue);
			} else {
				validUntil.setTime(validUntil.getTime() + durationValue * 30 * 24 * 60 * 60 * 1000);
			}
			break;
		case "y":
		case "Y":
			if (calendarAligned) {
				validUntil.setUTCFullYear(validUntil.getUTCFullYear() + durationValue);
			} else {
				validUntil.setTime(validUntil.getTime() + durationValue * 365 * 24 * 60 * 60 * 1000);
			}
			break;
	}
	return validUntil;
}

const shortDurationLabels: Record<string, string> = {
	"1m": "/min",
	"5m": "/5min",
	"15m": "/15min",
	"30m": "/30min",
	"1h": "/hr",
	"6h": "/6hr",
	"1d": "/day",
	"1w": "/wk",
	"1M": "/mo",
};

/**
 * Formats rate limit into compact display lines.
 * e.g. ["10K tokens/hr", "100 req/hr"]
 */
export function formatRateLimitLines(
	rateLimits:
		| {
				token_max_limit?: number | null;
				token_reset_duration?: string | null;
				request_max_limit?: number | null;
				request_reset_duration?: string | null;
		  }
		| null
		| undefined,
): string[] {
	if (!rateLimits) return [];
	const lines: string[] = [];
	if (rateLimits.token_max_limit != null) {
		const duration = rateLimits.token_reset_duration ?? "";
		const suffix = shortDurationLabels[duration] ?? (duration ? `/${duration}` : "");
		lines.push(`${formatCompactNumber(rateLimits.token_max_limit)} tokens${suffix}`);
	}
	if (rateLimits.request_max_limit != null) {
		const duration = rateLimits.request_reset_duration ?? "";
		const suffix = shortDurationLabels[duration] ?? (duration ? `/${duration}` : "");
		lines.push(`${formatCompactNumber(rateLimits.request_max_limit)} req${suffix}`);
	}
	return lines;
}

/**
 * Calculates usage percentage for rate limits
 */
export function calculateUsagePercentage(current: number, max: number): number {
	if (max === 0) return 0;
	return Math.round((current / max) * 100);
}

/**
 * Gets the appropriate variant for usage percentage badges
 */
export function getUsageVariant(percentage: number): "default" | "secondary" | "destructive" | "outline" {
	if (percentage >= 90) return "destructive";
	if (percentage >= 75) return "secondary";
	return "default";
}