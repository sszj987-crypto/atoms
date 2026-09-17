import React from "react";
import { render, screen } from "@testing-library/react";
import Home from "./page";
import { expect, test } from "vitest";
test("renders starter", () => { render(<Home />); expect(screen.getByText("Your app is ready.")).toBeInTheDocument(); });
