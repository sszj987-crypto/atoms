import type { NextConfig } from "next";
const nextConfig: NextConfig = { distDir: process.env.ATOMS_DEV === "1" ? ".next-dev" : ".next" };
export default nextConfig;
