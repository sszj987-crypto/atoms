import { Slot } from "@radix-ui/react-slot";
import type { ButtonHTMLAttributes } from "react";

export function Button({ asChild, ...props }: ButtonHTMLAttributes<HTMLButtonElement> & { asChild?: boolean }) {
  const Component = asChild ? Slot : "button";
  return <Component {...props} />;
}
