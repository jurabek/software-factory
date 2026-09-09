"use client";

import { useId, useState } from "react";
import { formatPlannerFeedback } from "@/client/planner-feedback.ts";
import { Button } from "@/components/ui/button.tsx";
import {
	Card,
	CardContent,
	CardDescription,
	CardFooter,
	CardHeader,
	CardTitle,
} from "@/components/ui/card.tsx";
import { Label } from "@/components/ui/label.tsx";
import { Textarea } from "@/components/ui/textarea.tsx";

type PlannerQuestionsProps = {
	questions: readonly string[];
	planDigest?: string;
	offline: boolean;
	pending: boolean;
	onSubmit: (feedback: string) => Promise<void> | void;
};

type PlannerQuestionsFormProps = Omit<PlannerQuestionsProps, "planDigest">;

function PlannerQuestionsForm({
	questions,
	offline,
	pending,
	onSubmit,
}: PlannerQuestionsFormProps) {
	const fieldIdPrefix = useId();
	const [answers, setAnswers] = useState<string[]>(() =>
		questions.map(() => ""),
	);
	const [submitting, setSubmitting] = useState(false);
	const allAnswered =
		answers.length === questions.length &&
		answers.every((answer) => answer.trim().length > 0);
	const disabled = offline || pending || submitting;

	async function submitAnswers(event: React.FormEvent<HTMLFormElement>) {
		event.preventDefault();
		if (disabled || !allAnswered) return;
		setSubmitting(true);
		try {
			await onSubmit(formatPlannerFeedback(questions, answers));
		} finally {
			setSubmitting(false);
		}
	}

	return (
		<Card>
			<CardHeader>
				<CardTitle id={`${fieldIdPrefix}-title`}>
					Planner needs your answers
				</CardTitle>
				<CardDescription>
					Answer every question before approving this plan.
				</CardDescription>
			</CardHeader>
			<form onSubmit={submitAnswers} aria-labelledby={`${fieldIdPrefix}-title`}>
				<CardContent>
					<ol className="flex flex-col gap-5">
						{questions.map((question, index) => {
							const fieldId = `${fieldIdPrefix}-answer-${index}`;
							return (
								<li
									className="flex flex-col gap-2"
									// biome-ignore lint/suspicious/noArrayIndexKey: Plan questions have no IDs and stay ordered for a digest.
									key={`${index}:${question}`}
								>
									<Label
										htmlFor={fieldId}
										className="items-start leading-relaxed"
									>
										<span
											className="text-primary font-semibold"
											aria-hidden="true"
										>
											{index + 1}.
										</span>
										<span>{question}</span>
									</Label>
									<Textarea
										id={fieldId}
										value={answers[index] ?? ""}
										onChange={(event) =>
											setAnswers((current) =>
												current.map((answer, answerIndex) =>
													answerIndex === index ? event.target.value : answer,
												),
											)
										}
										disabled={disabled}
										required
										aria-required="true"
										placeholder="Type your answer…"
									/>
								</li>
							);
						})}
					</ol>
				</CardContent>
				<CardFooter className="justify-end pt-6">
					<Button type="submit" disabled={disabled || !allAnswered}>
						{submitting || pending ? "Sending answers…" : "Send answers"}
					</Button>
				</CardFooter>
			</form>
		</Card>
	);
}

export function PlannerQuestions(props: PlannerQuestionsProps) {
	if (props.questions.length === 0) return null;
	const { planDigest, ...formProps } = props;
	return (
		<PlannerQuestionsForm
			key={planDigest ?? "planner-plan-without-digest"}
			{...formProps}
		/>
	);
}
