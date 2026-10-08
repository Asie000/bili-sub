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
		Short: "保存 SESSDATA/完整 Cookie 或扫码登录",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			sessdata, err := cmd.Flags().GetString("sessdata")
			if err != nil {
				return err
			}
			cookie, err := cmd.Flags().GetString("cookie")
			if err != nil {
				return err
			}
			qr, err := cmd.Flags().GetBool("qr")
			if err != nil {
				return err
			}
			// Validate before building the runtime: a bad invocation must not
			// create a data directory as a side effect.
			set := 0
			for _, given := range []bool{sessdata != "", cookie != "", qr} {
				if given {
					set++
				}
			}
			switch {
			case set == 0:
				return errors.New("login: 需要 --sessdata <值>、--cookie <值> 或 --qr")
			case set > 1:
				return errors.New("login: --sessdata、--cookie 与 --qr 只能选一个")
			}

			rt, err := newRuntime(cmd)
			if err != nil {
				return err
			}
			defer rt.close()
			out := cmd.OutOrStdout()
			var value, cookieValue string
			switch {
			case qr:
				client := bili.NewQRLoginClient()
				value, err = client.Login(cmd.Context(), func(qrURL string) {
					fmt.Fprintf(out, "请用哔哩哔哩 App 扫描二维码：%s\n", qrURL)
				})
				if err != nil {
					return err
				}
			case cookie != "":
				// Store the whole header verbatim and derive SESSDATA (which may
				// be empty) so the status/QR paths still work.
				value = bili.ExtractSESSDATA(cookie)
				cookieValue = cookie
			default:
				value = sessdata
			}

			sessionPath := rt.sessionPath()
			if err := bili.SaveSession(sessionPath, value, cookieValue); err != nil {
				return err
			}
			if cookieValue != "" {
				fmt.Fprintf(out, "完整 Cookie 已保存到 %s\n", sessionPath)
			} else {
				fmt.Fprintf(out, "SESSDATA 已保存到 %s\n", sessionPath)
			}

			client, err := rt.client()
			if err != nil {
				return err
			}
			ok, err := client.CheckLogin(cmd.Context())
			if err != nil {
				return fmt.Errorf("login: 校验失败: %w", err)
			}
			if !ok {
				return errors.New("login: 登录信息已保存，但校验未通过（未登录）")
			}
			fmt.Fprintln(out, "登录有效：校验通过。")
			return nil
		},
	}
	cmd.Flags().String("sessdata", "", "直接保存的 SESSDATA 值")
	cmd.Flags().String("cookie", "", "浏览器完整 Cookie（F12 → 网络 → 请求头 Cookie，整段复制）")
	cmd.Flags().Bool("qr", false, "扫码登录")
	return cmd
}
