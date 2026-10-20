export default function (pi) {
  pi.registerCommand("atlas-demo", {
    description: "Confirm that the Atlas demo Pi package is available",
    handler: async (_args, ctx) => {
      ctx.ui.notify("The Atlas demo Pi package is available.", "info");
    },
  });
}
