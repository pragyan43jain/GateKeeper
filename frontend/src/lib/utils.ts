import { clsx, type ClassValue } from "clsx";
import { extendTailwindMerge } from "tailwind-merge";
const merge = extendTailwindMerge({
  extend: {
    classGroups: {
      "font-size": [
        {
          text: [
            "figure",
            "title",
            "body",
            "sub",
            "dense",
            "meta",
            "chip",
            "micro",
          ],
        },
      ],
      shadow: [{ shadow: ["float", "overlay"] }],
    },
  },
});
export function cn(...values: ClassValue[]) {
  return merge(clsx(values));
}
