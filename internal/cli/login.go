package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"bilibili-sub/internal/bili"
)

func newLoginCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "login",
		Short: "保存 SESSDATA 或扫码登录",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			sessdata, err := cmd.Flags().GetString("sessdata")
			if err != nil {
				return err
			}
			qr, err := cmd.Flags().GetBool("qr")
			if err != nil {
				return err
			}
			// Validate before building the runtime: a bad invocation must not
			// create a data directory as a side effect.
			switch {
			case sessdata != "" && qr:
				return errors.New("login: --sessdata 与 --qr 只能选一个")
			case sessdata == "" && !qr:
				return errors.New("login: 需要 --sessdata <value> 或 --qr")
			}

			rt, err := newRuntime(cmd)
			if err != nil {
				return err
			}
			defer rt.close()
			out := cmd.OutOrStdout()
			value := sessdata
			if qr {
				client := bili.NewQRLoginClient()
				value, err = client.Login(cmd.Context(), func(qrURL string) {
					fmt.Fprintf(out, "请用哔哩哔哩 App 扫描二维码：%s\n", qrURL)
				})
				if err != nil {
					return err
				}
			}

			sessionPath := rt.sessionPath()
			if err := bili.SaveSESSDATA(sessionPath, value); err != nil {
				return err
			}
			fmt.Fprintf(out, "SESSDATA 已保存到 %s\n", sessionPath)

			client, err := rt.client()
			if err != nil {
				return err
			}
			ok, err := client.CheckLogin(cmd.Context())
			if err != nil {
				return fmt.Errorf("login: 校验失败: %w", err)
			}
			if !ok {
				return errors.New("login: SESSDATA 已保存，但校验未通过（未登录）")
			}
			fmt.Fprintln(out, "登录有效：SESSDATA 校验通过。")
			return nil
		},
	}
	cmd.Flags().String("sessdata", "", "直接保存的 SESSDATA 值")
	cmd.Flags().Bool("qr", false, "扫码登录")
	return cmd
}
